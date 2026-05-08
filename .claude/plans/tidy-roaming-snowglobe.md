# Polymorphic Relationships — Implementation Plan

## Context

Goravel currently exposes a partial polymorphic relationship surface: GORM-tag-based `MorphOne` / `MorphMany` work end-to-end (resolver, eager load, `Has*` / `WhereHas*Morph` existence queries, aggregate sub-selects). The remaining Eloquent/fedaco polymorphic feature set is missing:

- **MorphTo** (inverse polymorphic — child holds `<name>_type` + `<name>_id` and resolves to one of several parent types)
- **MorphToMany** and **MorphedByMany** (polymorphic many-to-many through a pivot)
- **MorphOne ofMany / latestOfMany / oldestOfMany** (one of many over a polymorphic relation)
- **Custom Polymorphic Types / Morph Map** (a global alias registry e.g. `{ "post": &Post{}, "video": &Video{} }`, used both at write time when storing `*_type` and at read time when dispatching `MorphTo` rows back to Go types)

Two hard constraints shape the design:

1. **GORM is unsuitable for declaring polymorphic relations.** Verified against `gorm.io/gorm v1.31.1` source at `~/go/pkg/mod/gorm.io/gorm@v1.31.1/schema/relationship.go:65–145, 195–261`:
   - `buildPolymorphicRelation` only branches off `polymorphic:` for HasOne/HasMany; the schema parser's `if/else if/else` chain is mutually exclusive with `many2many:`. **No polymorphic Many2Many support.**
   - There is no GORM tag for an inverse polymorphic — a `commentable_id` + `commentable_type` pair has no way to declare its variable parent set. **No MorphTo support.**
   - `polymorphicValue:` is a **per-field static tag**, not a registry — no global alias map.
   - Default value stored in the type column is `schema.Table` (the parent's table name).
   **Decision: GORM polymorphic tags are forbidden in this framework.** All five polymorphic kinds (`MorphOne`, `MorphMany`, `MorphTo`, `MorphToMany`, `MorphedByMany`) are declared via a single `ModelWithMorphRelations` interface, mirroring the existing `ModelWithThroughRelations` escape hatch at `contracts/database/orm/relation.go:173`. The resolver detects any leftover `gorm:"polymorphic:..."` tag and errors out with a clear message pointing the user at `MorphRelations()`. This is a breaking change for the small set of existing users on the GORM tag (test fixtures included) and is intentional — it eliminates the dual mechanism, makes the morph map the single source of truth for `*_type` values, and makes the contract uniform across all polymorphic kinds.

2. **GORM `Preload` is banned.** Verified: zero `.Preload(` call sites in `database/` or `contracts/`; the `With` doc comment at `contracts/database/orm/orm.go:236-237` states "*does not delegate to GORM Preload*". All eager loading is hand-built `WHERE … IN (?)` queries with chunking in `database/gorm/eager_loader.go:runEagerLoads`. Every new loader in this plan emits SELECT IN queries on `r.freshSession()` and dispatches via the existing `loadOneRelation` switch at `eager_loader.go:68`. No callers of `Preload` will be added.

The intended outcome is that all five Eloquent polymorphic features work through `Query.With(...)` / `Query.Load(...)` (eager) and `Query.Has*` / `Query.WhereHas*` (existence), consistent with the rest of the framework's API.

---

## Proposed change

### Phase 1 — Morph map registry (foundation)

Acts as the override point for the value used in every `*_type` column read/write. Without it, Phases 2 and 3 cannot resolve a stored type back to a Go model.

- **New** `contracts/database/orm/morph.go`:
  ```go
  // ModelWithMorphClass lets a model override the value written to and matched
  // against polymorphic *_type columns. Takes precedence over the morph map
  // and over GORM's default (table name).
  type ModelWithMorphClass interface {
      MorphClass() string
  }
  ```
- **New** `database/orm/morphmap/morphmap.go` — a process-wide registry (`sync.RWMutex`) in a sub-package because `database/orm` already imports `database/gorm`, so the storage must live somewhere both can import without a cycle. Exports `Register`, `Find`, `AliasOf`, `All`, `Reset`, `MorphValue`.
- **New** `database/orm/morph.go` — thin wrappers re-exporting the registry as the user-facing API: `orm.MorphMap`, `orm.MorphedModel`, `orm.MorphAlias`.
- **Resolution precedence** (matches fedaco `GetMorphClass` at `mixins/has-relationships.ts:166-181`):
    1. `MorphClass()` method on the model — **primary mechanism**, co-located with the model.
    2. Alias registered in the global morph map — fallback for third-party types or boot-time central registration.
    3. GORM-derived table name — **only when the model is unregistered**. (Note: GORM's `polymorphicValue:` tag is forbidden because the polymorphic tag itself is forbidden; see Phase 2.)
- **Wire-in points**:
  - In `database/gorm/relation.go`, add a helper `resolveMorphValue(parent, gormDefault)` and call it everywhere the descriptor is built, replacing direct reads of `rel.References[0].PrimaryValue`.
  - In `database/gorm/queries_relationships.go:464` (`HasMorph` builder), the per-type morph value comes from `tableNameFor(...)` today; route through `resolveMorphValue` so apps that registered aliases see them in `WHERE *_type = …` clauses.
- **Backward compatibility**: registry is opt-in. Existing apps that never call `MorphMap` and don't define `MorphClass()` continue to see the table name as the morph value, matching the prior default.

### Phase 2 — Replace GORM polymorphic tag with `MorphRelations()` method

GORM polymorphic tags (`gorm:"polymorphic:Houseable"`, `polymorphicType`, `polymorphicId`, `polymorphicValue`) are **forbidden**. All five polymorphic kinds (`MorphOne`, `MorphMany`, `MorphTo`, `MorphToMany`, `MorphedByMany`) declare metadata via a single interface mirroring `ModelWithThroughRelations`.

- **New contract** in `contracts/database/orm/relation.go`:
  ```go
  // MorphRelationKind enumerates the five polymorphic flavours.
  type MorphRelationKind string

  const (
      MorphOne      MorphRelationKind = "morphOne"
      MorphMany     MorphRelationKind = "morphMany"
      MorphTo       MorphRelationKind = "morphTo"
      MorphToMany   MorphRelationKind = "morphToMany"
      MorphedByMany MorphRelationKind = "morphedByMany"
  )

  // MorphRelation describes one polymorphic relationship. Field relevance depends on Kind:
  //
  //   MorphOne / MorphMany:        Related, Name, TypeColumn, IDColumn, LocalKey
  //   MorphTo:                     Name, TypeColumn, IDColumn, OwnerKey
  //   MorphToMany / MorphedByMany: Related, Name, Table, ForeignPivotKey, RelatedPivotKey,
  //                                ParentKey, RelatedKey, PivotColumns, PivotTimestamps
  //
  // Most fields default sensibly from Name; document which.
  type MorphRelation struct {
      Kind            MorphRelationKind
      Related         any
      Name            string
      TypeColumn      string
      IDColumn        string
      LocalKey        string
      OwnerKey        string
      Table           string
      ForeignPivotKey string
      RelatedPivotKey string
      ParentKey       string
      RelatedKey      string
      PivotColumns    []string
      PivotTimestamps bool
  }

  type ModelWithMorphRelations interface {
      MorphRelations() map[string]MorphRelation
  }
  ```
- **Resolver**: new `descriptorFromMorph(parent, name)` invoked **before** `descriptorFromGormRelation` in `resolveRelation` (`database/gorm/relation.go:75`). Switches on `MorphRelation.Kind` and populates the descriptor for each kind.
- **Forbid GORM polymorphic tags**: `descriptorFromGormRelation` errors with `errors.OrmPolymorphicTagForbidden` when `rel.Polymorphic != nil`, telling the user to declare the relation via `MorphRelations()`. The existing `case rel.Polymorphic != nil:` branch is removed.
- **Eager loaders** (in `database/gorm/eager_loader.go`):
  - `loadMorph` (existing at line 183) keeps working for `MorphOne`/`MorphMany` — only the descriptor source changes.
  - `loadMorphTo` (new) — algorithm mirrors fedaco `morph-to.ts:99-150`:
    1. Scan parents, group keys by the value of the `*_type` column → `map[morphValue][]parentRows`.
    2. For each group: resolve `morphValue` to a Go type via the morph map. Fail loudly with `errors.OrmMorphTypeUnknown` if unregistered.
    3. Run a SELECT IN per group keyed on `OwnerKey` against the resolved table (uses existing `runChunkedRelatedQuery` helper at `eager_loader.go:444`).
    4. Fan rows back via `setRelationField`. Parent-side field type is `any`.
  - `loadMorphToMany` (new) — mirrors `loadMany2Many` (line 233) with one extra `WHERE pivot.<name>_type = ?` on the intermediate query.
- **Existence** (in `database/gorm/queries_relationships.go`):
  - `MorphTo`: implement complete fedaco `hasMorph` semantics (`mixins/queries-relationships.ts:320-378`) — operator/count honoured per-type. Each requested type gets a synthetic `BelongsTo` subquery composed with `OR`:
    ```sql
    WHERE (
         (imageable_type = 'post'  AND (SELECT count(*) FROM posts  WHERE posts.id  = images.imageable_id) >= 5)
      OR (imageable_type = 'video' AND (SELECT count(*) FROM videos WHERE videos.id = images.imageable_id) >= 5)
    )
    ```
    Reuses existing `applyMorphExistence` (`queries_relationships.go:445`) plus the EXISTS / count switch at line 487.
  - `MorphToMany`: extend `compileExistenceSubquery` (line 521) with a `relKindMorphToMany` case adding the pivot type filter to the existing `Many2Many` JOIN.
- **Read-only scope for MorphToMany**: eager loading + existence only. Pivot attach/detach is out of scope.

### Phase 3 — Polymorphic One Of Many

Adds `OfMany`, `LatestOfMany`, `OldestOfMany` to a relation-scoped sub-builder. Falls naturally over the existing `MorphOne` (and unblocks `HasOne` for free, since fedaco's `mixinCanBeOneOfMany` at `concerns/can-be-one-of-many.ts:81-145` is shared between them).

- **Surface**: passed via the eager-load callback so it's local to a single relation:
  ```go
  q.With("LatestImage", func(q orm.Query) orm.Query {
      return q.LatestOfMany("created_at")
  })
  ```
- **Implementation**: rewrite the inner query to `INNER JOIN (SELECT MAX(<col>) … GROUP BY <fk>, <type_col>) sub ON …` against the related table. The morph type filter (`type_col = morphValue`) is added on both sides of the join. Lives in a new `database/gorm/one_of_many.go` and is invoked from `runRelatedQuery` (`eager_loader.go:403`) when the callback flagged the entry.
- **Scope**: implement for `MorphOne` and `HasOne` only in v1. `BelongsToMany.ofMany` is rare and can wait.

### Phase 4 — Errors, mocks, tests, docs

- **Errors** (per `CLAUDE.md` rule against inline `fmt.Errorf`): add to `errors/list.go` using the framework `New(...)` constructor:
  - `OrmMorphMapMissing`
  - `OrmMorphTypeUnknown` (raised when a row's `*_type` value is not in the morph map and the model has no `MorphClass()` override)
  - `OrmMorphRelationNotConfigured` (raised when `MorphRelations()` is missing or doesn't contain the requested name)
  - `OrmPolymorphicTagForbidden` (raised when a `gorm:"polymorphic:..."` tag is detected; message points the user at `MorphRelations()`)
  - `OrmMorphRelationKindUnknown` (raised when `MorphRelation.Kind` is not a recognised constant)
- **Mocks**: regenerate after editing `contracts/database/orm/`:
  ```bash
  go tool mockery
  ```
- **Tests** (follow `.agents/prompts/tests.md` — table-driven, `assert.*(t, *)`, `EXPECT().…Once()`):
  - Unit: extend `database/gorm/relation_test.go` with `descriptorFromMorphTo` / `descriptorFromMorphToMany` cases and morph-map override cases.
  - Unit: extend `database/gorm/eager_loader_test.go` with one happy + one missing-type test per kind.
  - Unit: extend `database/gorm/queries_relationships_test.go` with `Has`/`WhereHas` over MorphTo and MorphToMany.
  - Integration: add scenarios under `tests/` against the four real drivers (mysql, postgres, sqlite, sqlserver). The four `goravel/<driver>` external repos are unchanged by this plan — schema migrations use the existing blueprint helpers `Morphs`, `NullableMorphs`, `UuidMorphs`, `UlidMorphs` (`contracts/database/schema/blueprint.go:149-215`), already in place.
- **Docs**: update the `With` doc comment at `contracts/database/orm/orm.go:248` to add `MorphTo, MorphToMany, MorphedByMany`. Add a short README section in `database/orm/` describing the morph map. (Skip new top-level docs — codebase pattern is contract docstrings + tests.)

---

## Critical files

| File | Change |
|---|---|
| `contracts/database/orm/morph.go` (new) | `ModelWithMorphClass` interface |
| `contracts/database/orm/relation.go` | + `ModelWithMorphRelations`, `MorphToRelation`, `ModelWithMorphToManyRelations`, `MorphToManyRelation` |
| `database/orm/morphmap/morphmap.go` (new) | Process-wide registry (sub-package to avoid circular import — `database/orm` already imports `database/gorm`, so the storage must live somewhere both packages can import). Exports `Register`, `Find`, `AliasOf`, `All`, `Reset`, `MorphValue`. |
| `database/orm/morph.go` (new) | Thin wrappers re-exporting the registry as `orm.MorphMap`, `orm.MorphedModel`, `orm.MorphAlias` for app developers. |
| `database/gorm/relation.go` | + `relKindMorphTo`, `relKindMorphToMany`; new `descriptorFromMorphTo`, `descriptorFromMorphToMany`; new helper `resolveMorphValue(parent, gormDefault)` plumbed into the polymorphic case at line 143; new `parent any` parameter on `descriptorFromGormRelation` so the morph map override has access to the parent instance. |
| `database/gorm/eager_loader.go` | + `loadMorphTo`, `loadMorphToMany`; new switch arms in `loadOneRelation` (line 68) |
| `database/gorm/queries_relationships.go` | + `relKindMorphTo` + `relKindMorphToMany` cases in `compileExistenceSubquery` (line 521) and `applyExistence` (line 432); morph-map-aware `morphValueFor` in `applyMorphExistence` (line 464) |
| `database/gorm/one_of_many.go` (new) | Sub-query builder used by `runRelatedQuery` for `OfMany / LatestOfMany / OldestOfMany` |
| `errors/list.go` | + 5 named error constants |
| `mocks/` | regenerated via `go tool mockery` |

## Reuse — existing functions to lean on (don't reinvent)

- `runChunkedRelatedQuery` (`eager_loader.go:444`) — chunked SELECT IN with user-callback application. Used by every new loader.
- `chunkedFindMaps` (`eager_loader.go:460`) — for the pivot intermediate query in `MorphToMany`.
- `setRelationField` (`eager_loader.go:655`) — assigns rows to parent fields, handles `*Model` / `[]*Model` / `[]Model` shapes.
- `extractKeys`, `dictKey`, `parseGormSchema`, `tableNameFor`, `splitRelation`, `unwrapPointer`, `defaultStr` (`eager_loader.go`, `relation.go`) — reused as-is.
- `compileExistenceSubquery`'s `Many2Many` arm (`queries_relationships.go:537-544`) — model the `MorphToMany` arm on it, adding one extra pivot WHERE.
- `applyMorphExistence` (`queries_relationships.go:445`) — already does per-type fan-out with OR; mirror its conjunction handling for `MorphTo` existence.
- `errors.New(...)` constructor pattern from `errors/list.go` — mandatory for all new error declarations.

## Sequencing & sizing (recommended order)

1. **Phase 1** — Morph map registry + `MorphClass()` override + plumbing into descriptor build / morph existence sites. *Small, isolated.*
2. **Phase 2** — Replace GORM polymorphic tag with `MorphRelations()`. Adds new contract, all five kinds, eager loaders for MorphTo/MorphToMany, full `hasMorph` semantics for MorphTo, pivot type filter for MorphToMany. **Existing test fixtures using `gorm:"polymorphic:..."` will be migrated.** *Large; the centre of gravity of the work.*
3. **Phase 3** — One Of Many (covers HasOne and MorphOne). *Medium.* Independent of Phase 2's wire-up; can be skipped or deferred.

Pivot attach/detach for MorphToMany is **explicitly out of scope** for this plan.

## Verification

End-to-end checks before marking each phase complete:

```bash
# Phase 1
go test ./database/orm/...              # registry behaviour
go test ./database/gorm/ -run TestResolveRelation_MorphMap

# Phase 2
go test ./database/gorm/ -run TestLoadMorphTo
go test ./database/gorm/ -run TestHas_MorphTo

# Phase 3
go test ./database/gorm/ -run TestLoadMorphToMany
go test ./database/gorm/ -run TestHas_MorphToMany

# Phase 4
go test ./database/gorm/ -run TestOneOfMany

# Integration (each driver)
cd tests && go test ./... -run TestPolymorphic

# Full lint pass
golangci-lint run

# Regenerate and verify mocks compile
go tool mockery && go build ./...
```

Manual sanity: spin up the mysql test container per `tests/` README, define `Post`, `Video`, `Image{ImageableID, ImageableType}` and a `MorphRelations()` method on `Image`, then verify:
- `q.With("Imageable").Get(&images)` populates `image.Imageable` with the right concrete type
- `q.With("Images").Get(&posts)` (existing MorphMany path) still works unchanged → backward compat
- `Tag` + `Post.MorphToManyRelations()["Tags"]` → `q.With("Tags").Get(&posts)` returns the right tags via the polymorphic pivot

## Resolved decisions

- **MorphTo `Has` / `WhereHas` semantics**: implement the complete fedaco `hasMorph` (operator + count + per-type OR). Not simplified to `IS NOT NULL`. See Phase 2 "Existence".
- **Morph value resolution priority**: `MorphClass()` method > global morph map > GORM `polymorphicValue:` tag > GORM-derived table name. The model-level `MorphClass()` method is the primary mechanism — keeps morph aliases co-located with models and avoids scattered `MorphMap(...)` calls. The global registry is a fallback for third-party types.
- **Parent-side MorphTo field type**: `any`. A marker interface (e.g. `Morphable { MorphClass() string }`) was considered and rejected — it adds boilerplate without real type safety since the framework still needs the morph map to translate the stored string into a concrete Go type.
- **Pivot attach/detach for MorphToMany**: out of scope. v1 covers eager loading (`With`/`Load`) and existence queries (`Has`/`WhereHas`) only.

## Risks / open questions

- **Cross-connection morph types**. If a registered alias points at a model whose `Connection()` differs from the parent's, the loader must dispatch on each model's connection (mirroring fedaco at `morph-to.ts:188`). v1 should at least detect and error clearly via `OrmMorphCrossConnection`; full support can land in a follow-up.
- **Pivot table for MorphToMany**. GORM cannot describe it via tags, and `JoinTable` registration via `db.SetupJoinTable(...)` won't work since we don't go through GORM associations. We declare the pivot as a plain `Table` string in `MorphToManyRelation.Table` and never parse it as a model — same approach the framework already uses for ad-hoc joins.

---

# Follow-up: `NewRelation` + relation write operations

## Context

Phases 1–3 above ship the static side of relationships: declaration (`MorphRelations()`, `ThroughRelations()`), eager loading (`With` / `Load`), and existence queries (`Has` / `WhereHas*`). They cover the common request paths but leave two gaps that fedaco fills.

**Gap 1 — ad-hoc relation query.** Given a loaded parent and a relation name, get a `Query` already pre-scoped to "the related rows belonging to this parent". Without this, callers retype the FK/morph filters by hand:

```go
// Today: caller knows the columns
facades.Orm().Query().
    Where("imageable_id", post.ID).
    Where("imageable_type", "post").     // ← have to know the morph alias
    Where("status", "approved").
    Get(&images)
```

**Gap 2 — relation-aware writes.** Insert a child with the FK auto-filled, set/clear a BelongsTo or MorphTo, attach/detach pivot rows. Today the caller has to compute the FK and morph_type manually and either build raw SQL or chain `db.Where(...).Create(...)`-style calls — error-prone, especially for `MorphTo` where the morph alias has to be resolved against the morph map.

### Design constraint: Goravel is metadata-only — no Relation object

This is a hard constraint observed in the existing code. Look at how the framework treats relationships today:

- `relationDescriptor` (`database/gorm/relation.go:44-72`) is **internal** — never crosses a contract boundary.
- All per-kind logic (`loadHasOneOrMany`, `loadMorphTo`, `compileExistenceSubquery`, `applyOneOfManyJoin`) lives as methods on `*Query`, **not** on a Relation type.
- The user-facing surface is `Query.With()` / `Query.Load()` / `Query.Has()` — operations are invoked on `Query`, the relation name is just a string parameter, and metadata is consulted internally.
- `MorphRelations()` and `ThroughRelations()` are declarative metadata hooks, not factories that return a Relation object.

Importing fedaco's `Relation` class hierarchy (a query-builder-like object that also carries `attach`/`detach`/`save`) would split the framework into two abstractions (`Query` and `Relation`) — that's a step away from the established style.

A second observation that nails this down: **fedaco's write methods do not consult query state.** Verified at `relations/has-one-or-many.ts:176-179` — `save(model)` reads only `parent.GetAttribute(parentKey)` and calls `model.Save()`; the chain's `where(...)` is silently ignored. Same at `concerns/interacts-with-pivot-table.ts:338-348` for `_baseAttachRecord`. So even in fedaco a chain like `.NewRelation('comments').Where('foo', val).Save()` would drop the `Where`. Treating writes as chainable on a Relation object is misleading; they're independent SQL paths.

→ Both gaps should be filled by **top-level entrypoints on `Orm`** that consult metadata internally. No Relation interface, no Relation object — same pattern as `loadOneRelation` / `applyExistence`.

## Proposed change

### Surface

Add to `contracts/database/orm/orm.go`:

```go
type Orm interface {
    // ... existing methods ...

    // NewRelation returns a Query pre-scoped to the related rows for the given parent and
    // relation name. Caller can chain Where / OrderBy / Get / First / Count / etc. on it.
    // parent must be a non-nil pointer to a struct.
    //
    // Mirrors fedaco's model.NewRelation('foo') for the read path.
    // Write operations (save, attach, detach, etc.) are not chained off this Query — see the
    // dedicated Save / Associate / Attach / Detach / Sync / Toggle methods below.
    NewRelation(parent any, relation string) Query

    // --- HasOne / HasMany / MorphOne / MorphMany ---

    // Save inserts or updates child as a member of parent's relation. Sets the foreign key (and
    // morph_type for polymorphic) on child from parent's local key, then persists child.
    Save(parent any, relation string, child any) error
    // SaveMany is the slice form of Save.
    SaveMany(parent any, relation string, children any) error

    // --- BelongsTo / MorphTo ---

    // Associate sets parent's foreign key (and morph_type for MorphTo) so it points at the given
    // owner, then persists parent. The owner must be a saved model.
    Associate(parent any, relation string, owner any) error
    // Dissociate clears parent's foreign key (and morph_type for MorphTo) and persists parent.
    Dissociate(parent any, relation string) error

    // --- Many2Many / MorphToMany / MorphedByMany ---

    // Attach inserts pivot rows linking parent to each id in ids. For polymorphic pivots the
    // morph_type column is filled from the parent's morph value. ids may be either a slice of
    // scalar ids or a map[any]map[string]any of id -> per-row pivot attributes.
    Attach(parent any, relation string, ids any) error
    // Detach removes pivot rows linking parent to the given ids. Pass nil ids to remove all.
    Detach(parent any, relation string, ids ...any) error
    // Sync replaces parent's pivot rows so they exactly match ids: detaches missing entries,
    // attaches new ones, leaves existing untouched. Returns counts of attached/detached/updated
    // via the result struct.
    Sync(parent any, relation string, ids any) (*db.SyncResult, error)
    // SyncWithoutDetaching is Sync minus the detach step — adds missing entries only.
    SyncWithoutDetaching(parent any, relation string, ids any) (*db.SyncResult, error)
    // Toggle attaches missing entries, detaches existing ones.
    Toggle(parent any, relation string, ids any) (*db.SyncResult, error)
    // UpdateExistingPivot updates pivot columns for an already-attached id.
    UpdateExistingPivot(parent any, relation string, id any, attrs map[string]any) error
}
```

`db.SyncResult`: `{ Attached, Detached, Updated []any }` — a small new contract type alongside `db.Result`.

### Per-kind dispatch

Each top-level method is `parent any, relation string` plus operation args. Internal flow:

```
1. resolveRelation(r.instance, parent, relation) → desc
2. switch desc.kind
3. call kind-specific implementation, or error with OrmRelationKindNotSupported{op, kind}
```

| Method | Supported kinds | Behaviour |
|---|---|---|
| `NewRelation` | All | Returns Query with kind-specific WHERE / JOIN pre-applied (table below). |
| `Save` / `SaveMany` | HasOne, HasMany, MorphOne, MorphMany | Set FK (`<local_key>` → `<id_col>`); for morph also set `<type_col>` to `desc.morphValue`. Then persist via existing `Query.Save`. |
| `Associate` | BelongsTo, MorphTo | Set parent's FK column to owner's PK; for MorphTo also set parent's `<type_col>` to `morphmap.MorphValue(owner)` (error `OrmMorphTypeUnknown` if unregistered). Persist parent. |
| `Dissociate` | BelongsTo, MorphTo | Null FK (and `<type_col>` for MorphTo). Persist. |
| `Attach` | Many2Many, MorphToMany, MorphedByMany | INSERT pivot rows. For morph variants fill `<type_col>` with `desc.morphValue`. Skip ids that already have a pivot row (cheap pre-check via WHERE IN). |
| `Detach` | Many2Many, MorphToMany, MorphedByMany | DELETE matching pivot rows. With nil ids, delete all rows for the parent (and morph type). |
| `Sync` / `SyncWithoutDetaching` / `Toggle` | Many2Many, MorphToMany, MorphedByMany | Read current pivot rows for parent (+ morph type filter), diff against ids, run INSERT / DELETE accordingly. |
| `UpdateExistingPivot` | Many2Many, MorphToMany, MorphedByMany | UPDATE pivot WHERE pivot.parent_fk = parent.pk AND pivot.related_fk = id (+ morph type). |

`HasOneThrough` / `HasManyThrough` are **read-only** for both `NewRelation` and writes — calling `Save` / `Attach` / etc. on them errors with `OrmRelationKindNotSupported`.

### `NewRelation` — read path per kind

| Kind | Returned `Query` shape |
|---|---|
| HasOne / HasMany | `Query().Model(desc.relatedModel).Where("<id_col>", parent.<local_key>)` |
| BelongsTo | `Query().Model(desc.relatedModel).Where("<owner_key>", parent.<fk_col>)` |
| MorphOne / MorphMany | HasMany shape + `Where("<type_col>", desc.morphValue)` |
| MorphTo | Read parent's `<type_col>` and `<id_col>`. Resolve type via `morphmap.Find`; on miss return a guarded Query that yields no rows (`Where(raw, "1=0")`) and a documented `OrmMorphTypeUnknown` companion error returned via `Query.AddError`. On hit: `Query().Model(<resolved>).Where("<owner_key>", parent.<id_col>)`. |
| Many2Many | `Query().Table(desc.relatedTable).Joins("INNER JOIN <pivot> ON <related>.pk = <pivot>.related_fk").Where("<pivot>.<parent_fk>", parent.<pk>)` |
| MorphToMany / MorphedByMany | Many2Many shape + `Where("<pivot>.<type_col>", desc.morphValue)` |
| HasOneThrough / HasManyThrough | `Query().Table(desc.relatedTable).Joins("INNER JOIN <through> ON ...").Where("<through>.<first_key>", parent.<local_key>)` |

The Query returned has `Model(...)` set so subsequent unqualified `Where("col", v)` calls default to the related table's column space.

### Edge cases

- **Zero-valued FK on parent**: `image.ImageableID == 0` → returned Query's WHERE evaluates to `WHERE id = 0`; caller sees `OrmRecordNotFound` from `First` or `Count() == 0`. Same as fedaco.
- **MorphTo with empty `<type_col>` on parent**: Query is guarded with `Where(raw, "1=0")` and the parent's `*_id` is reported via `Query.AddError(OrmMorphTypeUnknown)` so `Get`/`First` returns no rows without a round-trip.
- **`Save` with a slice through `SaveMany`**: iterates and calls `Save` per element; bails on first error.
- **`Sync` returning detail counts**: callers who don't care can ignore the `*db.SyncResult` return value.
- **`Attach` when a pivot row already exists**: skipped (no duplicate key error). Documented behaviour.
- **Cross-connection writes (MorphTo)**: if `morphmap.Find(alias)` returns a model whose `Connection()` differs from the parent's, error with `OrmMorphCrossConnection` (already on the polymorphic-plan risks list).
- **Nested relations** (`"Books.Author"`): out of scope. Callers chain manually.
- **Slice of parents**: out of scope. All write methods take a single parent.

### Critical files

| File | Change |
|---|---|
| `contracts/database/orm/orm.go` | + 11 new methods on `Orm` (`NewRelation`, `Save`, `SaveMany`, `Associate`, `Dissociate`, `Attach`, `Detach`, `Sync`, `SyncWithoutDetaching`, `Toggle`, `UpdateExistingPivot`). |
| `contracts/database/db/result.go` (or similar location) | + `SyncResult` struct. |
| `database/orm/orm.go` | Implement the 11 methods on the concrete `Orm`; each delegates to a helper on the gorm `Query` (`r.Query().(*Query).<helper>(parent, relation, ...)`). |
| `database/gorm/new_relation.go` (new) | `(r *Query) newRelationQuery(parent, relation) Query` — read path. Switch on `desc.kind`, build the WHERE / JOIN. |
| `database/gorm/relation_writes.go` (new) | `(r *Query) saveThroughRelation(...)`, `associateThroughRelation(...)`, `attachThroughRelation(...)`, `detachThroughRelation(...)`, `syncThroughRelation(...)`. Each is a kind-switch dispatcher that errors on unsupported kinds. |
| `errors/list.go` | + `OrmNewRelationParentNotPointer`, `OrmRelationKindNotSupported` ("operation %q is not supported on relation %q (kind %q)"), `OrmMorphCrossConnection`. |
| `mocks/database/orm/Orm.go` | Regenerate via `go tool mockery`. |

### Reuse — existing functions

- `resolveRelation` (`database/gorm/relation.go:75`) — descriptor lookup for every kind.
- `parseGormSchema` + `field.ValueOf` (`database/gorm/eager_loader.go:828-836`) — read parent's column values.
- `morphmap.MorphValue` / `morphmap.Find` (`database/orm/morphmap/morphmap.go`) — alias resolution for morph operations.
- `compileExistenceSubquery` per-kind branches (`database/gorm/queries_relationships.go:521-657`) — same JOIN/WHERE shapes lifted into helpers.
- `Query.Save` / `Query.Update` / `Query.Delete` — final persistence step for each write operation.

### Verification

```bash
go test ./database/gorm/ -run "TestNewRelation|TestSaveThroughRelation|TestAttachThroughRelation|TestSyncThroughRelation"
go test ./database/orm/...
go tool mockery && go build ./...
go test ./tests/...   # integration against mysql/postgres/sqlite/sqlserver
```

Manual sanity (one example per write family):

```go
// HasMany Save: comment.PostID auto-filled, INSERT comment.
err := facades.Orm().Save(&post, "Comments", &comment)

// MorphTo Associate: image.ImageableID = post.ID, image.ImageableType = "post"; UPDATE image.
err := facades.Orm().Associate(&image, "Imageable", &post)

// MorphToMany Attach: INSERT INTO taggables (taggable_id, taggable_type, tag_id) VALUES (post.ID, 'post', ?), ...
err := facades.Orm().Attach(&post, "Tags", []any{1, 2, 3})

// Detach all: DELETE FROM taggables WHERE taggable_id = post.ID AND taggable_type = 'post'
err := facades.Orm().Detach(&post, "Tags")
```

### Sequencing

1. **Step 1** — `NewRelation` (read path) for all kinds. Tests. *Medium.*
2. **Step 2** — `Save` / `SaveMany` for HasOne / HasMany / MorphOne / MorphMany. Tests. *Small.*
3. **Step 3** — `Associate` / `Dissociate` for BelongsTo / MorphTo. Tests. *Small.*
4. **Step 4** — `Attach` / `Detach` for M2M variants. Tests. *Medium.*
5. **Step 5** — `Sync` / `SyncWithoutDetaching` / `Toggle` / `UpdateExistingPivot`. Tests. *Medium.*

Each step is independent; pause anywhere if priorities shift.

## Resolved decisions for this follow-up

- **No Relation interface / no Relation object.** All operations are top-level methods on `Orm`. Matches Goravel's existing metadata-only design. Verified against `loadOneRelation` / `applyExistence` precedent.
- **NewRelation returns Query, not Relation.** Read path only. Fedaco's `Relation` chain methods that mutate query state (`Where`, `OrderBy`, `Limit`) all funnel into Query in our equivalent; methods that don't consult query state (`save`, `attach`, etc.) become independent Orm methods.
- **`Save`, `Attach`, etc. ignore any pre-existing Query state.** Same as fedaco. Documented explicitly in each method's docstring.
- **HasOneThrough / HasManyThrough are read-only.** No `Save` / `Attach` semantics. Calling write methods errors with `OrmRelationKindNotSupported`.

## Open questions

- Should the M2M shape include `withPivot` columns by default? Recommend **no** in v1; keep the result strictly the related table. Add a follow-up `WithPivot(...)` later when there's a real ask.
- `Sync`/`Toggle` return type — `*db.SyncResult` or just `error`? Recommend the result struct for parity with Eloquent's `sync()` return. Cheap to add now, hard to add later without breaking callers.
- `Attach` accepting `map[any]map[string]any` for per-row pivot attributes — keep in v1 or defer to a separate `AttachWithAttributes`? Recommend keep — same ergonomics as Eloquent and trivial to implement (write a different INSERT row when the map form is detected).

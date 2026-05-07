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

package gorm

import (
	"reflect"

	gormio "gorm.io/gorm"

	contractsorm "github.com/goravel/framework/contracts/database/orm"
	"github.com/goravel/framework/database/orm/morphmap"
	"github.com/goravel/framework/errors"
	"github.com/goravel/framework/support/str"
)

// relationKind enumerates every relationship flavour the resolver can describe.
// It is a superset of GORM's RelationshipType because it also covers the inverse polymorphic
// (MorphTo) and the through relations declared via ModelWithThroughRelations.
type relationKind int

const (
	relKindHasOne relationKind = iota
	relKindHasMany
	relKindBelongsTo
	relKindMany2Many
	relKindMorphOne
	relKindMorphMany
	relKindMorphTo
	relKindMorphToMany
	relKindHasOneThrough
	relKindHasManyThrough
)

// referenceKey describes one column-pair from a GORM Reference, with each side already qualified
// by table name. PrimaryKey/ForeignKey naming follows GORM's convention.
type referenceKey struct {
	primaryTable  string
	primaryColumn string
	foreignTable  string
	foreignColumn string
}

// relationDescriptor is the resolver's normalised view of a relationship. It lets the
// queries-relationships builder construct correlated subqueries without ever calling back into
// GORM's relation internals.
type relationDescriptor struct {
	name         string
	kind         relationKind
	parentTable  string
	relatedTable string
	relatedModel any
	references   []referenceKey

	// many-to-many specifics
	pivotTable      string
	pivotParentRef  referenceKey
	pivotRelatedRef referenceKey

	// polymorphic specifics
	morphTypeColumn string // e.g. "imageable_type" — on parent table for MorphTo, on pivot for MorphToMany
	morphIDColumn   string // e.g. "imageable_id"  — on parent table for MorphTo, on pivot for MorphToMany
	morphValue      string // e.g. "post"          — used in WHERE *_type = ? filters
	morphOwnerKey   string // PK on each related model for MorphTo (defaults to "id")
	morphInverse    bool   // true for MorphedByMany — flips morph value source from parent to related

	// through specifics
	throughTable   string
	throughModel   any
	firstKey       string // FK on through pointing at parent
	secondKey      string // FK on related pointing at through
	localKey       string // PK on parent
	secondLocalKey string // PK on through

	// onQuery is the per-relation default scope from Relation.OnQuery. Applied by every code
	// path that builds an inner query for this relation (eager loaders, existence builders,
	// NewRelation), *before* any caller-supplied callback.
	onQuery contractsorm.RelationCallback

	// next link for nested resolution (e.g. "Books.Author")
	nested *relationDescriptor
}

// resolveRelation walks a (possibly dotted) relation path and returns a chain of descriptors
// rooted at the given parent model. The returned descriptor's nested field points at the next
// hop, so callers can recurse to build subqueries for "User.Books.Author"-style queries.
//
// All relations are declared via the parent's Relations() method (ModelWithRelations). GORM
// relation tags (`foreignKey`, `references`, `many2many`, `polymorphic`) are forbidden — if
// detected the resolver returns OrmRelationTagForbidden pointing the user at Relations().
func resolveRelation(db *gormio.DB, parent any, relation string) (*relationDescriptor, error) {
	if relation == "" {
		return nil, errors.OrmQueryEmptyRelation
	}

	head, tail := splitRelation(relation)

	// Parse the parent's schema using GORM's cache (avoids reparsing on every call).
	stmt := &gormio.Statement{DB: db}
	if err := stmt.Parse(parent); err != nil {
		return nil, err
	}
	parentSchema := stmt.Schema
	parentTable := parentSchema.Table

	// Detect forbidden GORM relation tags. If GORM populated a Relationships entry for the
	// requested name, the user has a conflicting tag — error out with a pointer to Relations().
	if _, hasGormRel := parentSchema.Relationships.Relations[head]; hasGormRel {
		return nil, errors.OrmRelationTagForbidden.Args(head, parentSchema.Name)
	}

	desc, err := descriptorFromRelations(db, parent, parentTable, head)
	if err != nil {
		return nil, err
	}
	desc.name = head

	if tail != "" {
		// Recurse using the *related* model as the new parent.
		nestedParent := desc.relatedModel
		if nestedParent == nil {
			return nil, errors.OrmRelationUnsupported.Args(head, parentSchema.Name, "no related model")
		}
		nested, err := resolveRelation(db, nestedParent, tail)
		if err != nil {
			return nil, err
		}
		desc.nested = nested
	}
	return desc, nil
}

// descriptorFromRelations resolves a relation declared via the parent's Relations() method.
// Handles all 11 kinds. Returns OrmRelationNotFound when the parent doesn't implement the
// interface or the relation name isn't in its map.
//
// Dispatch is by Go type, not by a discriminator field — each per-kind struct in
// contracts/database/orm satisfies the sealed Relation interface and lands in its own case here.
// The kinds form four families (mirroring fedaco's class hierarchy in
// /workbench/fedaco/libs/fedaco/src/fedaco/relations) which share resolver logic:
//
//   - HasOneOrMany family (HasOne, HasMany, MorphOne, MorphMany): FK lives on the related side.
//   - BelongsTo family (BelongsTo, MorphTo): FK lives on the parent.
//   - BelongsToMany family (Many2Many, MorphToMany, MorphedByMany): joined via a pivot table.
//   - HasManyThrough family (HasOneThrough, HasManyThrough): joined via an intermediate table.
//
// Family-level shared behaviour (Save/Associate/Attach/etc.) lives downstream in relation_writes.go
// and is dispatched by the internal relationKind groups; this function's job is to map the
// user-facing struct to the descriptor those downstream paths consume.
//
// Accepts Relations() declared with either a value receiver (`func (Foo) Relations()`) or a
// pointer receiver (`func (*Foo) Relations()`). Models often mix the two — e.g. value receivers
// for pure-metadata methods and pointer receivers for GORM lifecycle hooks — so the framework
// looks up the interface on whichever form is addressable.
func descriptorFromRelations(db *gormio.DB, parent any, parentTable, name string) (*relationDescriptor, error) {
	relations, ok := tryGetRelations(parent)
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}
	rel, ok := relations[name]
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}

	var (
		desc    *relationDescriptor
		err     error
		onQuery contractsorm.RelationCallback
	)
	switch r := rel.(type) {
	case contractsorm.HasOne:
		desc, err = descriptorFromHasOneOrMany(db, parent, parentTable, name, r.Related, r.ForeignKey, r.LocalKey, relKindHasOne)
		onQuery = r.OnQuery
	case contractsorm.HasMany:
		desc, err = descriptorFromHasOneOrMany(db, parent, parentTable, name, r.Related, r.ForeignKey, r.LocalKey, relKindHasMany)
		onQuery = r.OnQuery
	case contractsorm.BelongsTo:
		desc, err = descriptorFromBelongsTo(db, parent, parentTable, name, r)
		onQuery = r.OnQuery
	case contractsorm.Many2Many:
		desc, err = descriptorFromMany2Many(db, parent, parentTable, name, r)
		onQuery = r.OnQuery
	case contractsorm.MorphOne:
		desc, err = descriptorFromMorphOneOrMany(db, parent, parentTable, name, r.Related, r.Name, r.TypeColumn, r.IDColumn, r.LocalKey, relKindMorphOne)
		onQuery = r.OnQuery
	case contractsorm.MorphMany:
		desc, err = descriptorFromMorphOneOrMany(db, parent, parentTable, name, r.Related, r.Name, r.TypeColumn, r.IDColumn, r.LocalKey, relKindMorphMany)
		onQuery = r.OnQuery
	case contractsorm.MorphTo:
		desc, err = descriptorFromMorphTo(parent, parentTable, name, r)
		onQuery = r.OnQuery
	case contractsorm.MorphToMany:
		desc, err = descriptorFromMorphToMany(db, parent, parentTable, name, r, false)
		onQuery = r.OnQuery
	case contractsorm.MorphedByMany:
		desc, err = descriptorFromMorphedByMany(db, parent, parentTable, name, r)
		onQuery = r.OnQuery
	case contractsorm.HasOneThrough:
		desc, err = descriptorFromThrough(db, parent, parentTable, name, r.Related, r.Through, r.FirstKey, r.SecondKey, r.LocalKey, r.SecondLocalKey, relKindHasOneThrough)
		onQuery = r.OnQuery
	case contractsorm.HasManyThrough:
		desc, err = descriptorFromThrough(db, parent, parentTable, name, r.Related, r.Through, r.FirstKey, r.SecondKey, r.LocalKey, r.SecondLocalKey, relKindHasManyThrough)
		onQuery = r.OnQuery
	default:
		return nil, errors.OrmMorphRelationKindUnknown.Args(name, reflect.TypeOf(parent).String(), reflect.TypeOf(rel).String())
	}
	if err != nil {
		return nil, err
	}
	// Carry the per-relation default-scope hook into the descriptor; every consumer (eager
	// loader, existence builder, NewRelation) applies it before any caller callback.
	desc.onQuery = onQuery
	return desc, nil
}

// descriptorFromHasOneOrMany handles the HasOneOrMany family's non-polymorphic members
// (HasOne, HasMany). The polymorphic members (MorphOne, MorphMany) share the same FK-on-related
// shape but add a type-column filter, so they go through descriptorFromMorphOneOrMany instead.
func descriptorFromHasOneOrMany(db *gormio.DB, parent any, parentTable, name string, related any, foreignKey, localKey string, kind relationKind) (*relationDescriptor, error) {
	if related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	relatedTable, err := tableNameFor(db, related)
	if err != nil {
		return nil, err
	}
	fk := defaultStr(foreignKey, str.Of(parentTable).Singular().String()+"_id")
	lk := defaultStr(localKey, "id")
	return &relationDescriptor{
		kind:         kind,
		parentTable:  parentTable,
		relatedTable: relatedTable,
		relatedModel: related,
		references: []referenceKey{{
			primaryTable:  parentTable,
			primaryColumn: lk,
			foreignTable:  relatedTable,
			foreignColumn: fk,
		}},
	}, nil
}

func descriptorFromBelongsTo(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.BelongsTo) (*relationDescriptor, error) {
	if rel.Related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	relatedTable, err := tableNameFor(db, rel.Related)
	if err != nil {
		return nil, err
	}
	fk := defaultStr(rel.ForeignKey, str.Of(relatedTable).Singular().String()+"_id")
	owner := defaultStr(rel.OwnerKey, "id")
	return &relationDescriptor{
		kind:         relKindBelongsTo,
		parentTable:  parentTable,
		relatedTable: relatedTable,
		relatedModel: rel.Related,
		references: []referenceKey{{
			primaryTable:  relatedTable,
			primaryColumn: owner,
			foreignTable:  parentTable,
			foreignColumn: fk,
		}},
	}, nil
}

func descriptorFromMany2Many(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.Many2Many) (*relationDescriptor, error) {
	if rel.Related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	relatedTable, err := tableNameFor(db, rel.Related)
	if err != nil {
		return nil, err
	}
	parentSingular := str.Of(parentTable).Singular().String()
	relatedSingular := str.Of(relatedTable).Singular().String()
	pivotTable := defaultStr(rel.Table, alphabeticalPivotName(parentSingular, relatedSingular))
	foreignPivotKey := defaultStr(rel.ForeignPivotKey, parentSingular+"_id")
	relatedPivotKey := defaultStr(rel.RelatedPivotKey, relatedSingular+"_id")
	parentKey := defaultStr(rel.ParentKey, "id")
	relatedKey := defaultStr(rel.RelatedKey, "id")

	return &relationDescriptor{
		kind:         relKindMany2Many,
		parentTable:  parentTable,
		relatedTable: relatedTable,
		relatedModel: rel.Related,
		pivotTable:   pivotTable,
		pivotParentRef: referenceKey{
			primaryTable:  parentTable,
			primaryColumn: parentKey,
			foreignTable:  pivotTable,
			foreignColumn: foreignPivotKey,
		},
		pivotRelatedRef: referenceKey{
			primaryTable:  relatedTable,
			primaryColumn: relatedKey,
			foreignTable:  pivotTable,
			foreignColumn: relatedPivotKey,
		},
	}, nil
}

func descriptorFromMorphOneOrMany(db *gormio.DB, parent any, parentTable, name string, related any, morphName, typeCol, idCol, localKey string, kind relationKind) (*relationDescriptor, error) {
	if related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	if morphName == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	relatedTable, err := tableNameFor(db, related)
	if err != nil {
		return nil, err
	}
	typeColumn := defaultStr(typeCol, morphName+"_type")
	idColumn := defaultStr(idCol, morphName+"_id")
	lk := defaultStr(localKey, "id")

	return &relationDescriptor{
		kind:            kind,
		parentTable:     parentTable,
		relatedTable:    relatedTable,
		relatedModel:    related,
		morphTypeColumn: typeColumn,
		morphIDColumn:   idColumn,
		morphValue:      resolveMorphValue(parent, parentTable),
		references: []referenceKey{{
			primaryTable:  parentTable,
			primaryColumn: lk,
			foreignTable:  relatedTable,
			foreignColumn: idColumn,
		}},
	}, nil
}

func descriptorFromMorphTo(parent any, parentTable, name string, rel contractsorm.MorphTo) (*relationDescriptor, error) {
	if rel.Name == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	return &relationDescriptor{
		kind:            relKindMorphTo,
		parentTable:     parentTable,
		morphTypeColumn: defaultStr(rel.TypeColumn, rel.Name+"_type"),
		morphIDColumn:   defaultStr(rel.IDColumn, rel.Name+"_id"),
		morphOwnerKey:   defaultStr(rel.OwnerKey, "id"),
	}, nil
}

// descriptorFromMorphToMany covers MorphToMany. It's separated from MorphedByMany so the
// morph-value derivation source can differ (parent vs. related) without re-reading the kind via
// reflection.
func descriptorFromMorphToMany(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.MorphToMany, inverse bool) (*relationDescriptor, error) {
	return buildMorphPivotDescriptor(db, parent, parentTable, name,
		rel.Related, rel.Name, rel.Table, rel.TypeColumn,
		rel.ForeignPivotKey, rel.RelatedPivotKey, rel.ParentKey, rel.RelatedKey,
		inverse,
	)
}

func descriptorFromMorphedByMany(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.MorphedByMany) (*relationDescriptor, error) {
	return buildMorphPivotDescriptor(db, parent, parentTable, name,
		rel.Related, rel.Name, rel.Table, rel.TypeColumn,
		rel.ForeignPivotKey, rel.RelatedPivotKey, rel.ParentKey, rel.RelatedKey,
		true,
	)
}

func buildMorphPivotDescriptor(db *gormio.DB, parent any, parentTable, name string, related any, morphName, table, typeCol, foreignPivot, relatedPivot, parentKey, relatedKey string, inverse bool) (*relationDescriptor, error) {
	if related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	if morphName == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	relatedTable, err := tableNameFor(db, related)
	if err != nil {
		return nil, err
	}

	pivotTable := defaultStr(table, str.Of(morphName).Plural().String())
	morphTypeColumn := defaultStr(typeCol, morphName+"_type")
	morphIDColumn := defaultStr(foreignPivot, morphName+"_id")
	relatedPivotKey := defaultStr(relatedPivot, str.Of(relatedTable).Singular().String()+"_id")
	pk := defaultStr(parentKey, "id")
	rk := defaultStr(relatedKey, "id")

	morphValue := resolveMorphValue(parent, parentTable)
	if inverse {
		morphValue = resolveMorphValue(related, relatedTable)
	}

	return &relationDescriptor{
		kind:            relKindMorphToMany,
		parentTable:     parentTable,
		relatedTable:    relatedTable,
		relatedModel:    related,
		pivotTable:      pivotTable,
		morphTypeColumn: morphTypeColumn,
		morphIDColumn:   morphIDColumn,
		morphValue:      morphValue,
		morphInverse:    inverse,
		pivotParentRef: referenceKey{
			primaryTable:  parentTable,
			primaryColumn: pk,
			foreignTable:  pivotTable,
			foreignColumn: morphIDColumn,
		},
		pivotRelatedRef: referenceKey{
			primaryTable:  relatedTable,
			primaryColumn: rk,
			foreignTable:  pivotTable,
			foreignColumn: relatedPivotKey,
		},
	}, nil
}

func descriptorFromThrough(db *gormio.DB, parent any, parentTable, name string, related, through any, firstKey, secondKey, localKey, secondLocalKey string, kind relationKind) (*relationDescriptor, error) {
	if related == nil {
		return nil, errors.OrmRelationThroughNotConfigured.Args(name, reflect.TypeOf(parent).String())
	}
	if through == nil {
		return nil, errors.OrmRelationThroughNotConfigured.Args(name, reflect.TypeOf(parent).String())
	}
	relatedTable, err := tableNameFor(db, related)
	if err != nil {
		return nil, err
	}
	throughTable, err := tableNameFor(db, through)
	if err != nil {
		return nil, err
	}
	return &relationDescriptor{
		kind:           kind,
		parentTable:    parentTable,
		relatedTable:   relatedTable,
		relatedModel:   related,
		throughTable:   throughTable,
		throughModel:   through,
		firstKey:       defaultStr(firstKey, str.Of(parentTable).Singular().String()+"_id"),
		secondKey:      defaultStr(secondKey, str.Of(throughTable).Singular().String()+"_id"),
		localKey:       defaultStr(localKey, "id"),
		secondLocalKey: defaultStr(secondLocalKey, "id"),
	}, nil
}

// tableNameFor returns the GORM-resolved table name for any model instance.
func tableNameFor(db *gormio.DB, model any) (string, error) {
	stmt := &gormio.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return "", err
	}
	return stmt.Schema.Table, nil
}

// alphabeticalPivotName returns the Eloquent-convention default pivot table for a Many2Many
// relation: the two singular table names sorted alphabetically and joined by "_". E.g.
// (post, tag) -> "post_tag", (user, role) -> "role_user".
func alphabeticalPivotName(a, b string) string {
	if a < b {
		return a + "_" + b
	}
	return b + "_" + a
}

// splitRelation splits "Books.Author" into ("Books", "Author"). A non-dotted name yields ("X", "").
func splitRelation(relation string) (head, tail string) {
	for i := 0; i < len(relation); i++ {
		if relation[i] == '.' {
			return relation[:i], relation[i+1:]
		}
	}
	return relation, ""
}

func unwrapPointer(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return rv.Elem().Interface()
	}
	return v
}

func defaultStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// resolveMorphValue picks the value to use for a polymorphic *_type column. The model-level
// MorphClass() method takes precedence, then the global morph map (registered via orm.MorphMap),
// then GORM's parsed PrimaryValue (which is either a `polymorphicValue:` tag or the parent's
// table name).
func resolveMorphValue(parent any, gormDefault string) string {
	if v, ok := morphmap.MorphValue(parent); ok {
		return v
	}
	return gormDefault
}

// resolveMorphAlias returns the morph alias for model from MorphClass() / morph map only —
// without falling back to the table name. Used by Associate when we want to know whether the
// owner has an explicit registered alias before defaulting to its table.
func resolveMorphAlias(model any) (string, bool) {
	return morphmap.MorphValue(model)
}

// tryGetRelations returns the parent model's Relations() map regardless of whether the method is
// declared with a value receiver (`func (Foo) Relations()`) or a pointer receiver (`func (*Foo)
// Relations()`). Returns ok=false if neither form satisfies ModelWithRelations.
//
// Mirrors the dual-receiver detection in morphmap.tryMorphClass; both helpers exist because Go
// doesn't pick a receiver style for users — and real-world models freely mix value and pointer
// receivers across methods on the same struct.
func tryGetRelations(parent any) (map[string]contractsorm.Relation, bool) {
	// Direct interface satisfaction — covers the common case where parent is *Foo and Relations
	// has a pointer receiver, *or* parent is *Foo and Relations has a value receiver (since
	// pointer-to-T satisfies any value-receiver interface T).
	if m, ok := parent.(contractsorm.ModelWithRelations); ok {
		return m.Relations(), true
	}
	rv := reflect.ValueOf(parent)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, false
		}
		// Try the dereferenced value — covers value-receiver methods when parent is a pointer.
		// (Already handled above, but keep the branch for completeness; falls through silently.)
		if m, ok := rv.Elem().Interface().(contractsorm.ModelWithRelations); ok {
			return m.Relations(), true
		}
	case reflect.Struct:
		// parent is a value but Relations is on the pointer receiver — wrap in a fresh
		// addressable pointer so the method set includes the pointer-receiver methods.
		ptr := reflect.New(rv.Type())
		ptr.Elem().Set(rv)
		if m, ok := ptr.Interface().(contractsorm.ModelWithRelations); ok {
			return m.Relations(), true
		}
	}
	return nil, false
}

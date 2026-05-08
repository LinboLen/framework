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

// descriptorFromGormRelation handles HasOne, HasMany, BelongsTo and Many2Many — every
// non-polymorphic relation GORM's struct tags can describe. Polymorphic relations declared via
// `gorm:"polymorphic:..."` are forbidden; this function errors out with
// errors.OrmPolymorphicTagForbidden when it detects one. Polymorphic relations are declared via
// MorphRelations() and resolved by descriptorFromMorph instead.
// descriptorFromRelations resolves a relation declared via the parent's Relations() method.
// Handles all 11 kinds. Returns OrmRelationNotFound when the parent doesn't implement the
// interface or the relation name isn't in its map.
func descriptorFromRelations(db *gormio.DB, parent any, parentTable, name string) (*relationDescriptor, error) {
	holder, ok := unwrapPointer(parent).(contractsorm.ModelWithRelations)
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}
	rel, ok := holder.Relations()[name]
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}

	var (
		desc *relationDescriptor
		err  error
	)
	switch rel.Kind {
	case contractsorm.HasOne, contractsorm.HasMany:
		desc, err = descriptorFromHasOneOrMany(db, parent, parentTable, name, rel)
	case contractsorm.BelongsTo:
		desc, err = descriptorFromBelongsTo(db, parent, parentTable, name, rel)
	case contractsorm.Many2Many:
		desc, err = descriptorFromMany2Many(db, parent, parentTable, name, rel, false)
	case contractsorm.MorphOne, contractsorm.MorphMany:
		desc, err = descriptorFromMorphOneOrMany(db, parent, parentTable, name, rel)
	case contractsorm.MorphTo:
		desc, err = descriptorFromMorphTo(parent, parentTable, name, rel)
	case contractsorm.MorphToMany, contractsorm.MorphedByMany:
		desc, err = descriptorFromMorphToManyRel(db, parent, parentTable, name, rel)
	case contractsorm.HasOneThrough, contractsorm.HasManyThrough:
		desc, err = descriptorFromThroughRel(db, parent, parentTable, name, rel)
	default:
		return nil, errors.OrmMorphRelationKindUnknown.Args(name, reflect.TypeOf(parent).String(), string(rel.Kind))
	}
	if err != nil {
		return nil, err
	}
	// Carry the per-relation default-scope hook into the descriptor; every consumer (eager
	// loader, existence builder, NewRelation) applies it before any caller callback.
	desc.onQuery = rel.OnQuery
	return desc, nil
}

func descriptorFromHasOneOrMany(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.Relation) (*relationDescriptor, error) {
	if rel.Related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	relatedTable, err := tableNameFor(db, rel.Related)
	if err != nil {
		return nil, err
	}
	fk := defaultStr(rel.ForeignKey, str.Of(parentTable).Singular().String()+"_id")
	localKey := defaultStr(rel.LocalKey, "id")
	desc := &relationDescriptor{
		parentTable:  parentTable,
		relatedTable: relatedTable,
		relatedModel: rel.Related,
		references: []referenceKey{{
			primaryTable:  parentTable,
			primaryColumn: localKey,
			foreignTable:  relatedTable,
			foreignColumn: fk,
		}},
	}
	if rel.Kind == contractsorm.HasMany {
		desc.kind = relKindHasMany
	} else {
		desc.kind = relKindHasOne
	}
	return desc, nil
}

func descriptorFromBelongsTo(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.Relation) (*relationDescriptor, error) {
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

func descriptorFromMany2Many(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.Relation, isMorph bool) (*relationDescriptor, error) {
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

func descriptorFromMorphOneOrMany(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.Relation) (*relationDescriptor, error) {
	if rel.Related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	if rel.Name == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	relatedTable, err := tableNameFor(db, rel.Related)
	if err != nil {
		return nil, err
	}
	typeColumn := defaultStr(rel.TypeColumn, rel.Name+"_type")
	idColumn := defaultStr(rel.IDColumn, rel.Name+"_id")
	localKey := defaultStr(rel.LocalKey, "id")

	desc := &relationDescriptor{
		parentTable:     parentTable,
		relatedTable:    relatedTable,
		relatedModel:    rel.Related,
		morphTypeColumn: typeColumn,
		morphIDColumn:   idColumn,
		morphValue:      resolveMorphValue(parent, parentTable),
		references: []referenceKey{{
			primaryTable:  parentTable,
			primaryColumn: localKey,
			foreignTable:  relatedTable,
			foreignColumn: idColumn,
		}},
	}
	if rel.Kind == contractsorm.MorphMany {
		desc.kind = relKindMorphMany
	} else {
		desc.kind = relKindMorphOne
	}
	return desc, nil
}

func descriptorFromMorphTo(parent any, parentTable, name string, rel contractsorm.Relation) (*relationDescriptor, error) {
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

func descriptorFromMorphToManyRel(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.Relation) (*relationDescriptor, error) {
	if rel.Related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	if rel.Name == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	relatedTable, err := tableNameFor(db, rel.Related)
	if err != nil {
		return nil, err
	}

	pivotTable := defaultStr(rel.Table, str.Of(rel.Name).Plural().String())
	morphTypeColumn := defaultStr(rel.TypeColumn, rel.Name+"_type")
	morphIDColumn := defaultStr(rel.ForeignPivotKey, rel.Name+"_id")
	relatedPivotKey := defaultStr(rel.RelatedPivotKey, str.Of(relatedTable).Singular().String()+"_id")
	parentKey := defaultStr(rel.ParentKey, "id")
	relatedKey := defaultStr(rel.RelatedKey, "id")

	var morphValue string
	if rel.Kind == contractsorm.MorphedByMany {
		morphValue = resolveMorphValue(rel.Related, relatedTable)
	} else {
		morphValue = resolveMorphValue(parent, parentTable)
	}

	return &relationDescriptor{
		kind:            relKindMorphToMany,
		parentTable:     parentTable,
		relatedTable:    relatedTable,
		relatedModel:    rel.Related,
		pivotTable:      pivotTable,
		morphTypeColumn: morphTypeColumn,
		morphIDColumn:   morphIDColumn,
		morphValue:      morphValue,
		morphInverse:    rel.Kind == contractsorm.MorphedByMany,
		pivotParentRef: referenceKey{
			primaryTable:  parentTable,
			primaryColumn: parentKey,
			foreignTable:  pivotTable,
			foreignColumn: morphIDColumn,
		},
		pivotRelatedRef: referenceKey{
			primaryTable:  relatedTable,
			primaryColumn: relatedKey,
			foreignTable:  pivotTable,
			foreignColumn: relatedPivotKey,
		},
	}, nil
}

func descriptorFromThroughRel(db *gormio.DB, parent any, parentTable, name string, rel contractsorm.Relation) (*relationDescriptor, error) {
	if rel.Related == nil {
		return nil, errors.OrmRelationThroughNotConfigured.Args(name, reflect.TypeOf(parent).String())
	}
	if rel.Through == nil {
		return nil, errors.OrmRelationThroughNotConfigured.Args(name, reflect.TypeOf(parent).String())
	}
	relatedTable, err := tableNameFor(db, rel.Related)
	if err != nil {
		return nil, err
	}
	throughTable, err := tableNameFor(db, rel.Through)
	if err != nil {
		return nil, err
	}
	desc := &relationDescriptor{
		parentTable:    parentTable,
		relatedTable:   relatedTable,
		relatedModel:   rel.Related,
		throughTable:   throughTable,
		throughModel:   rel.Through,
		firstKey:       defaultStr(rel.FirstKey, str.Of(parentTable).Singular().String()+"_id"),
		secondKey:      defaultStr(rel.SecondKey, str.Of(throughTable).Singular().String()+"_id"),
		localKey:       defaultStr(rel.LocalKey, "id"),
		secondLocalKey: defaultStr(rel.SecondLocalKey, "id"),
	}
	if rel.Kind == contractsorm.HasManyThrough {
		desc.kind = relKindHasManyThrough
	} else {
		desc.kind = relKindHasOneThrough
	}
	return desc, nil
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

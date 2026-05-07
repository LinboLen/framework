package gorm

import (
	"reflect"

	gormio "gorm.io/gorm"
	gormschema "gorm.io/gorm/schema"

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

	// next link for nested resolution (e.g. "Books.Author")
	nested *relationDescriptor
}

// resolveRelation walks a (possibly dotted) relation path and returns a chain of descriptors
// rooted at the given parent model. The returned descriptor's nested field points at the next
// hop, so callers can recurse to build subqueries for "User.Books.Author"-style queries.
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

	// Polymorphic relations are declared via the MorphRelations() method and resolved before
	// GORM's parsed schema. This is the only path that can describe MorphTo / MorphToMany /
	// MorphedByMany; for MorphOne / MorphMany it also gives the framework an opt-out from
	// GORM's `polymorphic:` tag (which is forbidden — see descriptorFromGormRelation).
	desc, err := descriptorFromMorph(db, parent, parentTable, head)
	if err == nil {
		// Found via MorphRelations() declaration.
	} else if errors.Is(err, errors.OrmRelationNotFound) {
		// Fall through to GORM-parsed metadata.
		desc, err = descriptorFromGormRelation(parent, parentSchema, parentTable, head)
		if err == nil {
			// Found via GORM's parsed metadata.
		} else if errors.Is(err, errors.OrmRelationNotFound) {
			// Final fallback: ThroughRelations() declaration.
			desc, err = descriptorFromThrough(db, parent, parentTable, head)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	} else {
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
func descriptorFromGormRelation(parent any, parentSchema *gormschema.Schema, parentTable, name string) (*relationDescriptor, error) {
	rel, ok := parentSchema.Relationships.Relations[name]
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, parentSchema.Name)
	}

	if rel.Polymorphic != nil {
		return nil, errors.OrmPolymorphicTagForbidden.Args(name, parentSchema.Name)
	}

	related := rel.FieldSchema
	desc := &relationDescriptor{
		parentTable:  parentTable,
		relatedTable: related.Table,
		relatedModel: reflect.New(related.ModelType).Interface(),
	}

	switch {
	case rel.Type == gormschema.Many2Many && rel.JoinTable != nil:
		desc.kind = relKindMany2Many
		desc.pivotTable = rel.JoinTable.Table

		// In a Many2Many, References contains:
		//   - parent's PK <-> pivot's parent FK (OwnPrimaryKey == true)
		//   - related's PK <-> pivot's related FK (OwnPrimaryKey == false)
		for _, ref := range rel.References {
			rk := referenceKey{
				foreignTable:  desc.pivotTable,
				foreignColumn: ref.ForeignKey.DBName,
			}
			if ref.OwnPrimaryKey {
				rk.primaryTable = parentTable
				rk.primaryColumn = ref.PrimaryKey.DBName
				desc.pivotParentRef = rk
			} else {
				rk.primaryTable = related.Table
				rk.primaryColumn = ref.PrimaryKey.DBName
				desc.pivotRelatedRef = rk
			}
		}
		return desc, nil

	case rel.Type == gormschema.HasOne, rel.Type == gormschema.HasMany:
		for _, ref := range rel.References {
			desc.references = append(desc.references, referenceKey{
				primaryTable:  parentTable,
				primaryColumn: ref.PrimaryKey.DBName,
				foreignTable:  related.Table,
				foreignColumn: ref.ForeignKey.DBName,
			})
		}
		if rel.Type == gormschema.HasOne {
			desc.kind = relKindHasOne
		} else {
			desc.kind = relKindHasMany
		}
		return desc, nil

	case rel.Type == gormschema.BelongsTo:
		desc.kind = relKindBelongsTo
		// For BelongsTo, the FK lives on the *parent* table and the PK on the related table.
		for _, ref := range rel.References {
			desc.references = append(desc.references, referenceKey{
				primaryTable:  related.Table,
				primaryColumn: ref.PrimaryKey.DBName,
				foreignTable:  parentTable,
				foreignColumn: ref.ForeignKey.DBName,
			})
		}
		return desc, nil
	}

	return nil, errors.OrmRelationUnsupported.Args(name, parentSchema.Name, string(rel.Type))
}

// descriptorFromThrough resolves HasOneThrough / HasManyThrough relations declared by the model
// via the ModelWithThroughRelations interface. GORM has no struct-tag equivalent so we rely on
// an explicit declaration on the parent model.
func descriptorFromThrough(db *gormio.DB, parent any, parentTable, name string) (*relationDescriptor, error) {
	throughHolder, ok := unwrapPointer(parent).(contractsorm.ModelWithThroughRelations)
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}
	tr, ok := throughHolder.ThroughRelations()[name]
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}
	if tr.Related == nil || tr.Through == nil {
		return nil, errors.OrmRelationThroughNotConfigured.Args(name, reflect.TypeOf(parent).String())
	}

	relatedTable, err := tableNameFor(db, tr.Related)
	if err != nil {
		return nil, err
	}
	throughTable, err := tableNameFor(db, tr.Through)
	if err != nil {
		return nil, err
	}

	desc := &relationDescriptor{
		parentTable:    parentTable,
		relatedTable:   relatedTable,
		relatedModel:   tr.Related,
		throughTable:   throughTable,
		throughModel:   tr.Through,
		firstKey:       defaultStr(tr.FirstKey, "id"),
		secondKey:      defaultStr(tr.SecondKey, "id"),
		localKey:       defaultStr(tr.LocalKey, "id"),
		secondLocalKey: defaultStr(tr.SecondLocalKey, "id"),
	}
	switch tr.Kind {
	case contractsorm.HasOneThrough:
		desc.kind = relKindHasOneThrough
	case contractsorm.HasManyThrough:
		desc.kind = relKindHasManyThrough
	default:
		return nil, errors.OrmRelationUnsupported.Args(name, reflect.TypeOf(parent).String(), string(tr.Kind))
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

// descriptorFromMorph resolves a polymorphic relation declared via the parent's MorphRelations()
// method. Handles all five kinds: MorphOne, MorphMany, MorphTo, MorphToMany, MorphedByMany.
// Returns OrmRelationNotFound (wrapped) when the parent doesn't implement the interface or the
// relation name isn't in its map — letting the caller fall through to other resolvers.
func descriptorFromMorph(db *gormio.DB, parent any, parentTable, name string) (*relationDescriptor, error) {
	morphHolder, ok := unwrapPointer(parent).(contractsorm.ModelWithMorphRelations)
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}
	mr, ok := morphHolder.MorphRelations()[name]
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, reflect.TypeOf(parent).String())
	}

	switch mr.Kind {
	case contractsorm.MorphOne, contractsorm.MorphMany:
		return descriptorFromMorphOneOrMany(db, parent, parentTable, name, mr)
	case contractsorm.MorphTo:
		return descriptorFromMorphTo(parent, parentTable, name, mr)
	case contractsorm.MorphToMany, contractsorm.MorphedByMany:
		return descriptorFromMorphToMany(db, parent, parentTable, name, mr)
	default:
		return nil, errors.OrmMorphRelationKindUnknown.Args(name, reflect.TypeOf(parent).String(), string(mr.Kind))
	}
}

// descriptorFromMorphOneOrMany handles outbound MorphOne / MorphMany. The morph value comes from
// the parent (resolved via the morph map / MorphClass() method, falling back to the parent's
// table name).
func descriptorFromMorphOneOrMany(db *gormio.DB, parent any, parentTable, name string, mr contractsorm.MorphRelation) (*relationDescriptor, error) {
	if mr.Related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	if mr.Name == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	relatedTable, err := tableNameFor(db, mr.Related)
	if err != nil {
		return nil, err
	}
	typeColumn := defaultStr(mr.TypeColumn, mr.Name+"_type")
	idColumn := defaultStr(mr.IDColumn, mr.Name+"_id")
	localKey := defaultStr(mr.LocalKey, "id")

	desc := &relationDescriptor{
		parentTable:     parentTable,
		relatedTable:    relatedTable,
		relatedModel:    mr.Related,
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
	if mr.Kind == contractsorm.MorphMany {
		desc.kind = relKindMorphMany
	} else {
		desc.kind = relKindMorphOne
	}
	return desc, nil
}

// descriptorFromMorphTo handles the inverse polymorphic relation. The parent is the side that
// holds the morph_id + morph_type columns. The related model is determined per-row at load time
// by consulting the morph map; the descriptor therefore carries no relatedTable / relatedModel.
func descriptorFromMorphTo(parent any, parentTable, name string, mr contractsorm.MorphRelation) (*relationDescriptor, error) {
	if mr.Name == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	return &relationDescriptor{
		kind:            relKindMorphTo,
		parentTable:     parentTable,
		morphTypeColumn: defaultStr(mr.TypeColumn, mr.Name+"_type"),
		morphIDColumn:   defaultStr(mr.IDColumn, mr.Name+"_id"),
		morphOwnerKey:   defaultStr(mr.OwnerKey, "id"),
	}, nil
}

// descriptorFromMorphToMany handles MorphToMany and its inverse, MorphedByMany. The pivot table
// carries `<Name>_id` + `<Name>_type` plus the parent's foreign key. For MorphToMany the
// morph_type column pins on the parent's morph value; for MorphedByMany it pins on the related's
// morph value.
func descriptorFromMorphToMany(db *gormio.DB, parent any, parentTable, name string, mr contractsorm.MorphRelation) (*relationDescriptor, error) {
	if mr.Related == nil {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Related")
	}
	if mr.Name == "" {
		return nil, errors.OrmMorphRelationMissingField.Args(name, reflect.TypeOf(parent).String(), "Name")
	}
	relatedTable, err := tableNameFor(db, mr.Related)
	if err != nil {
		return nil, err
	}

	pivotTable := defaultStr(mr.Table, str.Of(mr.Name).Plural().String())
	morphTypeColumn := defaultStr(mr.TypeColumn, mr.Name+"_type")
	morphIDColumn := defaultStr(mr.ForeignPivotKey, mr.Name+"_id")
	relatedPivotKey := defaultStr(mr.RelatedPivotKey, str.Of(relatedTable).Singular().String()+"_id")
	parentKey := defaultStr(mr.ParentKey, "id")
	relatedKey := defaultStr(mr.RelatedKey, "id")

	// In the forward direction (MorphToMany), the morph_type column on the pivot stores the
	// parent's morph alias; in the inverse direction (MorphedByMany), it stores the related's.
	var morphValue string
	if mr.Kind == contractsorm.MorphedByMany {
		morphValue = resolveMorphValue(mr.Related, relatedTable)
	} else {
		morphValue = resolveMorphValue(parent, parentTable)
	}

	return &relationDescriptor{
		kind:            relKindMorphToMany,
		parentTable:     parentTable,
		relatedTable:    relatedTable,
		relatedModel:    mr.Related,
		pivotTable:      pivotTable,
		morphTypeColumn: morphTypeColumn,
		morphIDColumn:   morphIDColumn,
		morphValue:      morphValue,
		morphInverse:    mr.Kind == contractsorm.MorphedByMany,
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

package gorm

import (
	"reflect"

	gormio "gorm.io/gorm"
	gormschema "gorm.io/gorm/schema"

	contractsorm "github.com/goravel/framework/contracts/database/orm"
	"github.com/goravel/framework/errors"
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
	morphTypeColumn string // e.g. "houseable_type"
	morphIDColumn   string // e.g. "houseable_id"
	morphValue      string // e.g. "users"

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

	desc, err := descriptorFromGormRelation(parentSchema, parentTable, head)
	if err == nil {
		// Found via GORM's parsed metadata.
	} else if errors.Is(err, errors.OrmRelationNotFound) {
		// Fall back to the model's ThroughRelations() declaration.
		desc, err = descriptorFromThrough(db, parent, parentTable, head)
		if err != nil {
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

// descriptorFromGormRelation handles HasOne, HasMany, BelongsTo, Many2Many and the polymorphic
// HasOne/HasMany variants - everything GORM's struct tags can describe.
func descriptorFromGormRelation(parentSchema *gormschema.Schema, parentTable, name string) (*relationDescriptor, error) {
	rel, ok := parentSchema.Relationships.Relations[name]
	if !ok {
		return nil, errors.OrmRelationNotFound.Args(name, parentSchema.Name)
	}

	related := rel.FieldSchema
	desc := &relationDescriptor{
		parentTable:  parentTable,
		relatedTable: related.Table,
		relatedModel: reflect.New(related.ModelType).Interface(),
	}

	switch {
	case rel.Polymorphic != nil:
		// Polymorphic HasOne / HasMany. References has 2 entries:
		//   [0] PrimaryValue + ForeignKey (the type column)
		//   [1] PrimaryKey + ForeignKey  (the id column)
		if len(rel.References) < 2 {
			return nil, errors.OrmRelationUnsupported.Args(name, parentSchema.Name, "incomplete polymorphic references")
		}
		desc.morphTypeColumn = rel.References[0].ForeignKey.DBName
		desc.morphValue = rel.References[0].PrimaryValue
		desc.morphIDColumn = rel.References[1].ForeignKey.DBName
		desc.references = []referenceKey{
			{
				primaryTable:  parentTable,
				primaryColumn: rel.References[1].PrimaryKey.DBName,
				foreignTable:  related.Table,
				foreignColumn: rel.References[1].ForeignKey.DBName,
			},
		}
		if rel.Field.IndirectFieldType.Kind() == reflect.Slice {
			desc.kind = relKindMorphMany
		} else {
			desc.kind = relKindMorphOne
		}
		return desc, nil

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

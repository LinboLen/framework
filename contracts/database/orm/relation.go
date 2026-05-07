package orm

// RelationCallback is the signature of a closure used to scope a relationship existence query.
// Mirrors the (q) => void closure shape from the QueriesRelationships mixin. The returned Query
// is the inner subquery used for has / whereHas / withCount / etc.
type RelationCallback func(query Query) Query

// MorphRelationCallback is the per-type variant of RelationCallback used by the *HasMorph family,
// matching the `function ($query, $type)` callback for whereHasMorph. The second argument is the
// morph type currently being scoped (the related model's morph class - the table name in GORM's
// polymorphic convention).
type MorphRelationCallback func(query Query, morphType string) Query

// QueryWithRelations is the Go port of the QueriesRelationships mixin from Laravel and the 1:1
// TypeScript port in fedaco at libs/fedaco/src/fedaco/mixins/queries-relationships.ts.
//
// Where the upstream framework has first-class Relation objects with getRelationExistenceQuery
// methods, GORM models its relationships through struct-tag metadata. The bridge here surfaces
// relationship-existence and aggregate-subselect queries on top of that metadata. Callers can
// write, for example:
//
//	users := []User{}
//	query.Query().Has("Books", ">=", 3).WithCount("Roles").Get(&users)
//
// Existence-style methods (Has / OrHas / DoesntHave / WhereHas / ...) accept a variadic args
// slice that may carry, in any order:
//   - a RelationCallback or func(Query) Query to scope the inner subquery
//   - a string operator (e.g. ">=", "<", ">", "=") - defaults to ">="
//   - an int count - defaults to 1
//
// Morph-style methods take an additional types []any of model instances; the morph value used in
// the type column is derived from each model's GORM-resolved table name (e.g. *User -> "users").
//
// QueryWithRelations is embedded into Query, so all of these methods are also reachable directly
// off Query without a type assertion.
type QueryWithRelations interface {
	// Has adds a relationship count / exists condition to the query.
	// Defaults to operator ">=" and count 1.
	Has(relation string, args ...any) Query
	// OrHas adds a relationship count / exists condition to the query with an "or" conjunction.
	OrHas(relation string, args ...any) Query
	// DoesntHave adds a relationship absence condition - equivalent to Has(rel, "<", 1).
	DoesntHave(relation string, args ...any) Query
	// OrDoesntHave adds a relationship absence condition with an "or" conjunction.
	OrDoesntHave(relation string, args ...any) Query
	// WhereHas adds a relationship count / exists condition to the query with where clauses.
	// Identical semantics to Has but conventionally used with a callback first arg.
	WhereHas(relation string, args ...any) Query
	// OrWhereHas adds a relationship count / exists condition to the query with where clauses
	// and an "or" conjunction.
	OrWhereHas(relation string, args ...any) Query
	// WhereDoesntHave adds a relationship absence condition to the query with where clauses.
	WhereDoesntHave(relation string, args ...any) Query
	// OrWhereDoesntHave adds a relationship absence condition to the query with where clauses
	// and an "or" conjunction.
	OrWhereDoesntHave(relation string, args ...any) Query

	// HasMorph adds a polymorphic relationship count / exists condition to the query.
	// types is a slice of model instances (e.g. []any{&Post{}, &Video{}}); the morph value
	// used in the type column is derived from each model's table name.
	//
	// Note: auto-discovery of distinct morph values via `types = ['*']` is not supported.
	// An explicit list of model instances is required.
	HasMorph(relation string, types []any, args ...any) Query
	// OrHasMorph adds a polymorphic relationship count / exists condition with an "or" conjunction.
	OrHasMorph(relation string, types []any, args ...any) Query
	// DoesntHaveMorph adds a polymorphic relationship absence condition.
	DoesntHaveMorph(relation string, types []any, args ...any) Query
	// OrDoesntHaveMorph adds a polymorphic relationship absence condition with an "or" conjunction.
	OrDoesntHaveMorph(relation string, types []any, args ...any) Query
	// WhereHasMorph adds a polymorphic relationship count / exists condition to the query with
	// where clauses. Callbacks may be MorphRelationCallback for per-type scoping.
	WhereHasMorph(relation string, types []any, args ...any) Query
	// OrWhereHasMorph adds a polymorphic relationship count / exists condition with where clauses
	// and an "or" conjunction.
	OrWhereHasMorph(relation string, types []any, args ...any) Query
	// WhereDoesntHaveMorph adds a polymorphic relationship absence condition with where clauses.
	WhereDoesntHaveMorph(relation string, types []any, args ...any) Query
	// OrWhereDoesntHaveMorph adds a polymorphic relationship absence condition with where clauses
	// and an "or" conjunction.
	OrWhereDoesntHaveMorph(relation string, types []any, args ...any) Query

	// WithAggregate adds a sub-select query to include an aggregate value for a relationship.
	// fn must be one of: count, max, min, sum, avg, exists.
	WithAggregate(relation, column, fn string, args ...any) Query
	// WithCount adds sub-select queries to count the relations. Each entry may be either a
	// string ("Books") or a RelationCount struct for scoped/aliased counts.
	WithCount(relations ...any) Query
	// WithMax adds sub-select queries to include the max of the relation's column.
	WithMax(relation, column string, args ...any) Query
	// WithMin adds sub-select queries to include the min of the relation's column.
	WithMin(relation, column string, args ...any) Query
	// WithSum adds sub-select queries to include the sum of the relation's column.
	WithSum(relation, column string, args ...any) Query
	// WithAvg adds sub-select queries to include the average of the relation's column.
	WithAvg(relation, column string, args ...any) Query
	// WithExists adds sub-select queries to include the existence of related models. The result
	// is emitted as `CASE WHEN EXISTS (...) THEN 1 ELSE 0 END` for cross-dialect portability
	// (SQL Server has no boolean literal), but the dest field may be either `bool` or an integer
	// type - Go's database/sql layer converts 0/1 ints to bool automatically.
	WithExists(relations ...string) Query
}

// RelationCount is an entry accepted by WithCount that pairs a relation name with an optional
// scope callback and result alias. Equivalent to the array-keyed `withCount(['posts as p_count' =>
// fn ...])` idiom in Laravel, expressed as a Go struct:
//
//	q.WithCount(orm.RelationCount{Name: "Books", Alias: "book_total", Callback: func(q) q.Where(...)})
type RelationCount struct {
	// Name is the relation method/field name on the parent model (e.g. "Books").
	Name string
	// Alias overrides the default `<relation>_count` column alias when non-empty.
	Alias string
	// Callback scopes the inner count subquery, mirroring the upstream array-keyed callback shape.
	Callback RelationCallback
}

// ThroughRelationKind enumerates the supported "through" relation flavours. These have no
// equivalent in GORM's struct-tag schema, so they are declared via ModelWithThroughRelations.
type ThroughRelationKind string

const (
	// HasOneThrough corresponds to the HasOneThrough relation.
	HasOneThrough ThroughRelationKind = "hasOneThrough"
	// HasManyThrough corresponds to the HasManyThrough relation.
	HasManyThrough ThroughRelationKind = "hasManyThrough"
)

// ThroughRelation describes a HasOneThrough / HasManyThrough relationship that GORM cannot infer
// from struct tags. A model declares its through relations by implementing
// ModelWithThroughRelations. Field semantics match the upstream HasManyThrough constructor:
//
//	new HasManyThrough($query, $farParent, $throughParent, $firstKey, $secondKey, $localKey, $secondLocalKey)
//
// Example: Country has many Posts through User.
//
//	type Country struct{ ... }
//
//	func (Country) ThroughRelations() map[string]orm.ThroughRelation {
//	    return map[string]orm.ThroughRelation{
//	        "Posts": {
//	            Kind:           orm.HasManyThrough,
//	            Related:        &Post{},
//	            Through:        &User{},
//	            FirstKey:       "country_id", // foreign key on Through pointing at Parent (Country)
//	            SecondKey:      "user_id",    // foreign key on Related pointing at Through (User)
//	            LocalKey:       "id",         // local primary key on Parent
//	            SecondLocalKey: "id",         // local primary key on Through
//	        },
//	    }
//	}
type ThroughRelation struct {
	// Kind selects between HasOneThrough and HasManyThrough.
	Kind ThroughRelationKind
	// Related is a sample instance of the related model (e.g. &Post{}).
	Related any
	// Through is a sample instance of the intermediate model (e.g. &User{}).
	Through any
	// FirstKey is the foreign key on Through that references Parent's LocalKey.
	FirstKey string
	// SecondKey is the foreign key on Related that references Through's SecondLocalKey.
	SecondKey string
	// LocalKey is the local primary key on Parent (defaults to "id" when empty).
	LocalKey string
	// SecondLocalKey is the local primary key on Through (defaults to "id" when empty).
	SecondLocalKey string
}

// ModelWithThroughRelations is implemented by models that declare HasOneThrough / HasManyThrough
// relationships. The map is keyed by the relation name used at call sites (e.g. q.Has("Posts")).
// This is the only way Goravel can resolve through-relations because GORM has no struct-tag
// representation of them.
type ModelWithThroughRelations interface {
	ThroughRelations() map[string]ThroughRelation
}

// MorphRelationKind enumerates the five polymorphic relation kinds. All polymorphic relations in
// Goravel are declared via ModelWithMorphRelations; GORM `polymorphic:` struct tags are
// forbidden because they cannot express the inverse direction (MorphTo) and the polymorphic
// many-to-many flavours (MorphToMany / MorphedByMany).
type MorphRelationKind string

const (
	// MorphOne is the single-result outbound polymorphic relation. The related model holds
	// `<Name>_id` and `<Name>_type` columns referencing the parent.
	MorphOne MorphRelationKind = "morphOne"
	// MorphMany is the multi-result outbound polymorphic relation.
	MorphMany MorphRelationKind = "morphMany"
	// MorphTo is the inverse polymorphic relation: the model holds `<Name>_id` + `<Name>_type`
	// and resolves to one of several parent types via the morph map registry.
	MorphTo MorphRelationKind = "morphTo"
	// MorphToMany is a polymorphic many-to-many through a pivot table that carries
	// `<Name>_id` + `<Name>_type` plus the parent's foreign key.
	MorphToMany MorphRelationKind = "morphToMany"
	// MorphedByMany is the inverse of MorphToMany — the side reached *through* a polymorphic
	// many-to-many. Same pivot, but the morph value pins on the related side.
	MorphedByMany MorphRelationKind = "morphedByMany"
)

// MorphRelation describes one polymorphic relationship. Field relevance depends on Kind:
//
//	MorphOne / MorphMany:        Related, Name, TypeColumn, IDColumn, LocalKey
//	MorphTo:                     Name, TypeColumn, IDColumn, OwnerKey
//	MorphToMany / MorphedByMany: Related, Name, Table, ForeignPivotKey, RelatedPivotKey,
//	                             ParentKey, RelatedKey, PivotColumns, PivotTimestamps
//
// Most fields default sensibly when left zero — see field-level docstrings for the exact rule.
//
// Example — outbound MorphMany declared on the parent:
//
//	func (Post) MorphRelations() map[string]orm.MorphRelation {
//	    return map[string]orm.MorphRelation{
//	        "Images": {Kind: orm.MorphMany, Related: &Image{}, Name: "imageable"},
//	    }
//	}
//
// Example — inverse MorphTo declared on the child:
//
//	type Image struct {
//	    ImageableID   uint
//	    ImageableType string
//	    Imageable     any  // populated by the eager loader with *Post / *Video / etc.
//	}
//
//	func (Image) MorphRelations() map[string]orm.MorphRelation {
//	    return map[string]orm.MorphRelation{
//	        "Imageable": {Kind: orm.MorphTo, Name: "imageable"},
//	    }
//	}
type MorphRelation struct {
	// Kind selects the polymorphic flavour. Required.
	Kind MorphRelationKind

	// Related is a sample instance of the related model (e.g. &Image{}). Required for every
	// kind except MorphTo, where the related type is resolved per-row via the morph map.
	Related any

	// Name is the polymorphic name (e.g. "imageable", "taggable"). Required. Used to derive
	// default column names: TypeColumn defaults to "<Name>_type", IDColumn to "<Name>_id",
	// and pivot fields when zero.
	Name string

	// TypeColumn is the morph type column on the related table (or pivot, for MorphToMany).
	// Defaults to "<Name>_type" when empty.
	TypeColumn string

	// IDColumn is the morph id column on the related table. Defaults to "<Name>_id" when empty.
	IDColumn string

	// LocalKey is the parent column that the related's *_id references. Defaults to "id".
	// Used by MorphOne / MorphMany.
	LocalKey string

	// OwnerKey is the related-table column that *_id references. Defaults to "id".
	// Used by MorphTo.
	OwnerKey string

	// Table is the pivot table name for MorphToMany / MorphedByMany. When empty defaults to
	// the plural snake-case of Name (e.g. "taggable" -> "taggables").
	Table string

	// ForeignPivotKey is the pivot column referencing the parent (or, for MorphedByMany, the
	// related). Defaults to "<Name>_id".
	ForeignPivotKey string

	// RelatedPivotKey is the pivot column referencing the related (or, for MorphedByMany, the
	// parent). Defaults to "<related-table-singular>_id".
	RelatedPivotKey string

	// ParentKey is the parent column referenced by ForeignPivotKey. Defaults to "id".
	ParentKey string

	// RelatedKey is the related column referenced by RelatedPivotKey. Defaults to "id".
	RelatedKey string

	// PivotColumns are extra pivot columns to surface on loaded results.
	PivotColumns []string

	// PivotTimestamps, when true, expects created_at / updated_at columns on the pivot.
	PivotTimestamps bool
}

// ModelWithMorphRelations is implemented by models that declare polymorphic relationships. The
// map is keyed by the relation name used at call sites (e.g. q.With("Imageable")).
//
// This is the *only* way Goravel resolves polymorphic relationships. The corresponding GORM
// struct-tag mechanism (`gorm:"polymorphic:..."`) is forbidden because it cannot express the
// inverse direction or polymorphic many-to-many flavours.
type ModelWithMorphRelations interface {
	MorphRelations() map[string]MorphRelation
}

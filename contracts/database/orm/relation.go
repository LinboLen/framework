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
	// is yielded as a 0/1 integer column - Goravel does not have a $casts-style sugar to coerce
	// the value to a boolean automatically.
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

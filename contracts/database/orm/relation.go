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

// RelationKind names a relationship flavour for diagnostic / error-message use only. The
// per-kind structs below (HasOne, HasMany, ...) are the actual user-facing declaration types;
// the RelationKind constants exist purely so error messages can refer to a kind by name.
type RelationKind string

const (
	KindHasOne         RelationKind = "hasOne"
	KindHasMany        RelationKind = "hasMany"
	KindBelongsTo      RelationKind = "belongsTo"
	KindMany2Many      RelationKind = "many2Many"
	KindMorphOne       RelationKind = "morphOne"
	KindMorphMany      RelationKind = "morphMany"
	KindMorphTo        RelationKind = "morphTo"
	KindMorphToMany    RelationKind = "morphToMany"
	KindMorphedByMany  RelationKind = "morphedByMany"
	KindHasOneThrough  RelationKind = "hasOneThrough"
	KindHasManyThrough RelationKind = "hasManyThrough"
)

// Relation is the sealed interface implemented by every per-kind relation declaration struct
// (HasOne, HasMany, BelongsTo, Many2Many, MorphOne, MorphMany, MorphTo, MorphToMany,
// MorphedByMany, HasOneThrough, HasManyThrough). The relation() method is unexported so external
// packages cannot define new kinds — the resolver type-switches on the closed set defined here.
//
// Models declare their relationships in a single map:
//
//	func (User) Relations() map[string]orm.Relation {
//	    return map[string]orm.Relation{
//	        "Books":   orm.HasMany{Related: &Book{}},
//	        "Roles":   orm.Many2Many{Related: &Role{}, Table: "user_roles"},
//	        "Houses":  orm.MorphMany{Related: &House{}, Name: "houseable"},
//	        "Posts":   orm.HasManyThrough{Related: &Post{}, Through: &Account{}},
//	    }
//	}
//
// All relation fields on the model struct must be tagged `gorm:"-"` so GORM doesn't try to
// auto-resolve them from struct tags.
type Relation interface {
	// Kind returns the relation flavour for diagnostics (error messages, logging). The resolver
	// itself dispatches by Go type, not by the Kind value.
	Kind() RelationKind
}

// HasOne declares a one-to-one relation where the related row holds a foreign key referencing
// this model.
//
// Defaults: ForeignKey = singular(parentTable) + "_id"; LocalKey = "id".
type HasOne struct {
	// Related is a sample instance of the related model (e.g. &Profile{}).
	Related any
	// ForeignKey is the column on the related table referencing the parent. Optional.
	ForeignKey string
	// LocalKey is the column on the parent referenced by ForeignKey. Optional, defaults to "id".
	LocalKey string
	// OnQuery is a default scope applied to every query built for this relation (eager loads,
	// existence checks, aggregates, NewRelation). Applied before any caller-supplied callback.
	OnQuery RelationCallback
}

func (HasOne) Kind() RelationKind { return KindHasOne }

// HasMany declares a one-to-many relation — the multi-result variant of HasOne.
//
// Defaults: ForeignKey = singular(parentTable) + "_id"; LocalKey = "id".
type HasMany struct {
	Related    any
	ForeignKey string
	LocalKey   string
	OnQuery    RelationCallback
}

func (HasMany) Kind() RelationKind { return KindHasMany }

// BelongsTo declares the inverse of HasOne / HasMany — this model holds a foreign key
// referencing the related row.
//
// Defaults: ForeignKey = singular(relatedTable) + "_id"; OwnerKey = "id".
type BelongsTo struct {
	Related any
	// ForeignKey is the column on the parent table referencing the related row. Optional.
	ForeignKey string
	// OwnerKey is the column on the related table referenced by ForeignKey. Optional, "id".
	OwnerKey string
	OnQuery  RelationCallback
}

func (BelongsTo) Kind() RelationKind { return KindBelongsTo }

// Many2Many declares a many-to-many relation through a pivot table.
//
// Defaults:
//
//	Table            = alphabetical singular pair (e.g. "post_tag")
//	ForeignPivotKey  = singular(parentTable) + "_id"
//	RelatedPivotKey  = singular(relatedTable) + "_id"
//	ParentKey        = "id"
//	RelatedKey       = "id"
type Many2Many struct {
	Related any
	// Table is the pivot table name. Optional.
	Table string
	// ForeignPivotKey is the pivot column referencing the parent. Optional.
	ForeignPivotKey string
	// RelatedPivotKey is the pivot column referencing the related. Optional.
	RelatedPivotKey string
	// ParentKey is the column on the parent referenced by ForeignPivotKey. Optional, "id".
	ParentKey string
	// RelatedKey is the column on the related referenced by RelatedPivotKey. Optional, "id".
	RelatedKey string
	// PivotColumns are extra pivot columns to surface on eager-loaded results via the related
	// model's `Pivot orm.Pivot` field. See the Pivot type alias for the read-side hydration
	// convention.
	PivotColumns []string
	// PivotTimestamps, when true, expects created_at / updated_at on the pivot table; the
	// framework auto-stamps them on Attach / Sync / Save and updated_at on UpdateExistingPivot.
	PivotTimestamps bool
	// PivotCreatedAt overrides the created_at column name on the pivot table. Default "created_at".
	// Only consulted when PivotTimestamps is true.
	PivotCreatedAt string
	// PivotUpdatedAt overrides the updated_at column name on the pivot table. Default "updated_at".
	// Only consulted when PivotTimestamps is true.
	PivotUpdatedAt string
	OnQuery        RelationCallback
}

func (Many2Many) Kind() RelationKind { return KindMany2Many }

// MorphOne declares a one-to-one polymorphic relation — the related row holds <Name>_id and
// <Name>_type referencing one of several possible parent kinds.
//
// Defaults: TypeColumn = Name + "_type"; IDColumn = Name + "_id"; LocalKey = "id".
type MorphOne struct {
	Related any
	// Name is the polymorphic name (e.g. "imageable", "taggable"). Required.
	Name string
	// TypeColumn is the polymorphic type column on the related table. Optional.
	TypeColumn string
	// IDColumn is the polymorphic id column on the related table. Optional.
	IDColumn string
	// LocalKey is the column on the parent referenced by IDColumn. Optional, "id".
	LocalKey string
	OnQuery  RelationCallback
}

func (MorphOne) Kind() RelationKind { return KindMorphOne }

// MorphMany is the multi-result variant of MorphOne.
type MorphMany struct {
	Related    any
	Name       string
	TypeColumn string
	IDColumn   string
	LocalKey   string
	OnQuery    RelationCallback
}

func (MorphMany) Kind() RelationKind { return KindMorphMany }

// MorphTo declares the inverse polymorphic side: this model holds <Name>_id + <Name>_type and
// resolves to one of several parent kinds via the morph map registry. There is no Related — the
// concrete type is determined per row from the type column.
//
// Defaults: TypeColumn = Name + "_type"; IDColumn = Name + "_id"; OwnerKey = "id".
type MorphTo struct {
	// Name is the polymorphic name. Required.
	Name string
	// TypeColumn is the polymorphic type column on this table. Optional.
	TypeColumn string
	// IDColumn is the polymorphic id column on this table. Optional.
	IDColumn string
	// OwnerKey is the column on each related table referenced by IDColumn. Optional, "id".
	OwnerKey string
	OnQuery  RelationCallback
}

func (MorphTo) Kind() RelationKind { return KindMorphTo }

// MorphToMany declares a polymorphic many-to-many — through a pivot that carries
// <Name>_id + <Name>_type plus a related FK.
//
// Defaults:
//
//	Table            = pluralize(Name)  (e.g. "taggables")
//	TypeColumn       = Name + "_type"
//	ForeignPivotKey  = Name + "_id"
//	RelatedPivotKey  = singular(relatedTable) + "_id"
//	ParentKey        = "id"
//	RelatedKey       = "id"
type MorphToMany struct {
	Related         any
	Name            string
	Table           string
	TypeColumn      string
	ForeignPivotKey string
	RelatedPivotKey string
	ParentKey       string
	RelatedKey      string
	PivotColumns    []string
	PivotTimestamps bool
	PivotCreatedAt  string
	PivotUpdatedAt  string
	OnQuery         RelationCallback
}

func (MorphToMany) Kind() RelationKind { return KindMorphToMany }

// MorphedByMany is the inverse side of MorphToMany — the morph value pins on the related rather
// than the parent. Field semantics and defaults match MorphToMany.
type MorphedByMany struct {
	Related         any
	Name            string
	Table           string
	TypeColumn      string
	ForeignPivotKey string
	RelatedPivotKey string
	ParentKey       string
	RelatedKey      string
	PivotColumns    []string
	PivotTimestamps bool
	PivotCreatedAt  string
	PivotUpdatedAt  string
	OnQuery         RelationCallback
}

func (MorphedByMany) Kind() RelationKind { return KindMorphedByMany }

// HasOneThrough declares a relation reached through an intermediate ("through") table.
//
// Defaults:
//
//	FirstKey       = singular(parentTable) + "_id"
//	SecondKey      = singular(throughTable) + "_id"
//	LocalKey       = "id"
//	SecondLocalKey = "id"
type HasOneThrough struct {
	Related any
	// Through is the intermediate model.
	Through any
	// FirstKey is the FK on the through table pointing at parent. Optional.
	FirstKey string
	// SecondKey is the FK on the related table pointing at through. Optional.
	SecondKey string
	// LocalKey is the PK on the parent referenced by FirstKey. Optional, "id".
	LocalKey string
	// SecondLocalKey is the PK on the through table referenced by SecondKey. Optional, "id".
	SecondLocalKey string
	OnQuery        RelationCallback
}

func (HasOneThrough) Kind() RelationKind { return KindHasOneThrough }

// HasManyThrough is the multi-result variant of HasOneThrough.
type HasManyThrough struct {
	Related        any
	Through        any
	FirstKey       string
	SecondKey      string
	LocalKey       string
	SecondLocalKey string
	OnQuery        RelationCallback
}

func (HasManyThrough) Kind() RelationKind { return KindHasManyThrough }

// Pivot is the carrier for extra pivot-table columns surfaced on eager-loaded models in the
// BelongsToMany family (Many2Many, MorphToMany, MorphedByMany). When a related model declares
// a `Pivot orm.Pivot` field tagged `gorm:"-"`, the eager loader pulls every column listed in the
// relation's PivotColumns (plus the timestamp columns when PivotTimestamps is true) and writes
// them into that field keyed by column name.
//
// Example:
//
//	type Role struct {
//	    ID    uint
//	    Name  string
//	    Pivot orm.Pivot `gorm:"-"`   // populated by With("Roles") / Load("Roles")
//	}
//
// The convention is eager-load only — NewRelation(parent, "Roles").Get(&roles) returns the bare
// related rows without populating the Pivot field. Callers that need ad-hoc pivot reads should
// use eager loading.
type Pivot = map[string]any

// ModelWithRelations is implemented by every model that declares relationships. The single map
// returned by Relations() is the only place relations are declared. GORM relation struct tags
// (`foreignKey:`, `references:`, `many2many:`, `polymorphic:`) are forbidden — fields that hold
// related rows must be tagged `gorm:"-"`.
type ModelWithRelations interface {
	Relations() map[string]Relation
}

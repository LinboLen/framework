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

// RelationKind enumerates every supported relationship flavour. All relations are declared via
// a single ModelWithRelations interface; GORM relation struct tags (`foreignKey:`, `references:`,
// `many2many:`, `polymorphic:`) are forbidden because they couldn't express the full surface and
// because spreading metadata across two declaration mechanisms made model definitions hard to
// audit.
type RelationKind string

const (
	// HasOne — the related row holds a foreign key referencing this model.
	HasOne RelationKind = "hasOne"
	// HasMany — multi-result HasOne.
	HasMany RelationKind = "hasMany"
	// BelongsTo — this model holds a foreign key referencing the related row.
	BelongsTo RelationKind = "belongsTo"
	// Many2Many — through a pivot table whose rows pair this model and the related.
	Many2Many RelationKind = "many2Many"
	// MorphOne — the related row holds <Name>_id and <Name>_type referencing one of several
	// possible parent kinds.
	MorphOne RelationKind = "morphOne"
	// MorphMany — multi-result MorphOne.
	MorphMany RelationKind = "morphMany"
	// MorphTo — this model holds <Name>_id + <Name>_type and resolves to one of several parent
	// kinds via the morph map registry.
	MorphTo RelationKind = "morphTo"
	// MorphToMany — through a pivot that carries <Name>_id + <Name>_type plus a related FK.
	MorphToMany RelationKind = "morphToMany"
	// MorphedByMany — the inverse side of MorphToMany.
	MorphedByMany RelationKind = "morphedByMany"
	// HasOneThrough — like HasOne but reached through an intermediate ("through") table.
	HasOneThrough RelationKind = "hasOneThrough"
	// HasManyThrough — multi-result HasOneThrough.
	HasManyThrough RelationKind = "hasManyThrough"
)

// Relation describes one relationship. Field relevance depends on Kind — fields not relevant to
// the chosen kind are ignored. When optional fields are left zero, the framework fills them
// using snake_case naming conventions (singular table name + "_id", etc.).
//
// Required fields per kind:
//
//   - HasOne / HasMany / BelongsTo / Many2Many:   Related
//   - MorphOne / MorphMany / MorphToMany / MorphedByMany: Related, Name
//   - MorphTo:                                    Name (Related is resolved per-row via morph map)
//   - HasOneThrough / HasManyThrough:             Related, Through
//
// Default values for optional fields:
//
//   - LocalKey / ParentKey / OwnerKey / RelatedKey / SecondLocalKey: "id"
//   - HasOne / HasMany ForeignKey:        singular(parentTable) + "_id"
//   - BelongsTo ForeignKey:               singular(relatedTable) + "_id"
//   - Many2Many Table:                    alphabetical singular pair, e.g. "post_tag"
//   - Many2Many ForeignPivotKey:          singular(parentTable) + "_id"
//   - Many2Many RelatedPivotKey:          singular(relatedTable) + "_id"
//   - MorphOne / MorphMany / MorphTo TypeColumn: Name + "_type"
//   - MorphOne / MorphMany / MorphTo IDColumn:   Name + "_id"
//   - MorphToMany / MorphedByMany Table:         pluralize(Name)  e.g. "taggables"
//   - MorphToMany / MorphedByMany ForeignPivotKey: Name + "_id"
//   - HasOneThrough / HasManyThrough FirstKey:   singular(parentTable) + "_id"
//   - HasOneThrough / HasManyThrough SecondKey:  singular(throughTable) + "_id"
//
// Example — a single User declaration:
//
//	func (User) Relations() map[string]orm.Relation {
//	    return map[string]orm.Relation{
//	        "Books":  {Kind: orm.HasMany,        Related: &Book{}},
//	        "Roles":  {Kind: orm.Many2Many,      Related: &Role{}, Table: "user_roles"},
//	        "Houses": {Kind: orm.MorphMany,      Related: &House{}, Name: "houseable"},
//	        "Posts":  {Kind: orm.HasManyThrough, Related: &Post{},  Through: &Account{}},
//	    }
//	}
//
// All relation fields on the model struct must be tagged `gorm:"-"` so GORM doesn't try to
// auto-resolve them from struct tags.
type Relation struct {
	// Kind selects the relation flavour. Required for every entry.
	Kind RelationKind

	// Related is a sample instance of the related model (e.g. &Book{}). Required for all
	// kinds except MorphTo.
	Related any

	// Name is the polymorphic name (e.g. "imageable", "taggable"). Required for the five
	// polymorphic kinds; ignored otherwise.
	Name string

	// ForeignKey is the column on the related table for HasOne / HasMany, or on this table
	// for BelongsTo. Defaults are described above.
	ForeignKey string

	// LocalKey is the column on the parent referenced by ForeignKey. Defaults to "id".
	LocalKey string

	// OwnerKey is the column on the related referenced by ForeignKey for BelongsTo / MorphTo.
	// Defaults to "id".
	OwnerKey string

	// TypeColumn is the polymorphic type column. Defaults to <Name> + "_type".
	TypeColumn string

	// IDColumn is the polymorphic id column. Defaults to <Name> + "_id".
	IDColumn string

	// Table is the pivot table for Many2Many / MorphToMany / MorphedByMany. Default rules
	// described above.
	Table string

	// ForeignPivotKey is the pivot column referencing the parent (or related, for inverse
	// MorphedByMany). Defaults described above.
	ForeignPivotKey string

	// RelatedPivotKey is the pivot column referencing the related (or parent, for inverse).
	// Defaults to singular(relatedTable) + "_id".
	RelatedPivotKey string

	// ParentKey is the column on the parent referenced by ForeignPivotKey for the M2M family.
	// Defaults to "id".
	ParentKey string

	// RelatedKey is the column on the related referenced by RelatedPivotKey for the M2M family.
	// Defaults to "id".
	RelatedKey string

	// PivotColumns are extra pivot columns to surface on read.
	PivotColumns []string

	// PivotTimestamps, when true, expects created_at / updated_at on the pivot table.
	PivotTimestamps bool

	// Through is the intermediate model for HasOneThrough / HasManyThrough.
	Through any

	// FirstKey is the FK on the through table pointing at parent. Default:
	// singular(parentTable) + "_id".
	FirstKey string

	// SecondKey is the FK on the related table pointing at through. Default:
	// singular(throughTable) + "_id".
	SecondKey string

	// SecondLocalKey is the PK on the through table referenced by SecondKey. Defaults to "id".
	SecondLocalKey string

	// OnQuery is a default scope applied to every query the framework builds for this relation
	// — eager loads (With / Load), existence checks (Has / WhereHas), aggregates (WithCount /
	// WithSum / etc.) and ad-hoc lookups (Orm.NewRelation). Receives the inner query *after* the
	// per-kind FK / morph filters have been applied; returns the (possibly modified) query that
	// the framework will continue with.
	//
	// Applied before any caller-supplied callback so the OnQuery scope is always in effect, and
	// callers can layer extra conditions on top via With("Books", func(q) { ... }).
	//
	// Typical use — only ever load published comments for any Post relation chain:
	//
	//	"Comments": {
	//	    Kind: orm.HasMany, Related: &Comment{},
	//	    OnQuery: func(q orm.Query) orm.Query { return q.Where("published", true) },
	//	},
	//
	// Mirrors fedaco's `onQuery` decorator option (libs/fedaco/src/annotation/relation-column.ts:17).
	OnQuery RelationCallback
}

// ModelWithRelations is implemented by every model that declares relationships. The single map
// returned by Relations() is the only place relations are declared. GORM relation struct tags
// (`foreignKey:`, `references:`, `many2many:`, `polymorphic:`) are forbidden — fields that hold
// related rows must be tagged `gorm:"-"`.
type ModelWithRelations interface {
	Relations() map[string]Relation
}


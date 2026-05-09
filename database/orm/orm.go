package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/goravel/framework/contracts/binding"
	"github.com/goravel/framework/contracts/config"
	"github.com/goravel/framework/contracts/database"
	dbcontract "github.com/goravel/framework/contracts/database/db"
	contractsorm "github.com/goravel/framework/contracts/database/orm"
	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/database/driver"
	"github.com/goravel/framework/database/factory"
	"github.com/goravel/framework/database/gorm"
)

type Orm struct {
	ctx             context.Context
	config          config.Config
	log             log.Log
	query           contractsorm.Query
	queries         map[string]contractsorm.Query
	fresh           func(key ...any)
	connection      string
	modelToObserver []contractsorm.ModelToObserver
	dbConfig        database.Config
	mutex           sync.Mutex
}

func NewOrm(
	ctx context.Context,
	config config.Config,
	connection string,
	dbConfig database.Config,
	query contractsorm.Query,
	queries map[string]contractsorm.Query,
	log log.Log,
	modelToObserver []contractsorm.ModelToObserver,
	fresh func(key ...any),
) *Orm {
	return &Orm{
		ctx:             ctx,
		config:          config,
		connection:      connection,
		dbConfig:        dbConfig,
		log:             log,
		modelToObserver: modelToObserver,
		query:           query,
		queries:         queries,
		fresh:           fresh,
	}
}

func BuildOrm(ctx context.Context, config config.Config, connection string, log log.Log, fresh func(key ...any)) (*Orm, error) {
	query, dbConfig, err := gorm.BuildQuery(ctx, config, connection, log, nil)
	if err != nil {
		return NewOrm(ctx, config, connection, dbConfig, nil, nil, log, nil, fresh), err
	}

	queries := map[string]contractsorm.Query{
		connection: query,
	}

	return NewOrm(ctx, config, connection, dbConfig, query, queries, log, nil, fresh), nil
}

func (r *Orm) Config() database.Config {
	return r.dbConfig
}

func (r *Orm) Connection(name string) contractsorm.Orm {
	if name == "" {
		name = r.config.GetString("database.default")
	}
	if instance, exist := r.queries[name]; exist {
		return NewOrm(r.ctx, r.config, name, r.dbConfig, instance, r.queries, r.log, r.modelToObserver, r.fresh)
	}

	query, dbConfig, err := gorm.BuildQuery(r.ctx, r.config, name, r.log, r.modelToObserver)
	if err != nil || query == nil {
		r.log.Errorf("[Orm] Init %s connection error: %v", name, err)

		return NewOrm(r.ctx, r.config, name, dbConfig, nil, r.queries, r.log, r.modelToObserver, r.fresh)
	}

	r.queries[name] = query

	return NewOrm(r.ctx, r.config, name, dbConfig, query, r.queries, r.log, r.modelToObserver, r.fresh)
}

func (r *Orm) DB() (*sql.DB, error) {
	return r.query.DB()
}

func (r *Orm) Factory() contractsorm.Factory {
	return factory.NewFactoryImpl(r.Query())
}

func (r *Orm) DatabaseName() string {
	return r.dbConfig.Database
}

func (r *Orm) Name() string {
	return r.dbConfig.Connection
}

func (r *Orm) Observe(model any, observer contractsorm.Observer) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.modelToObserver = append(r.modelToObserver, contractsorm.ModelToObserver{
		Model:    model,
		Observer: observer,
	})

	for _, query := range r.queries {
		if queryWithObserver, ok := query.(contractsorm.QueryWithObserver); ok {
			queryWithObserver.Observe(model, observer)
		}
	}

	if queryWithObserver, ok := r.query.(contractsorm.QueryWithObserver); ok {
		queryWithObserver.Observe(model, observer)
	}
}

func (r *Orm) Query() contractsorm.Query {
	if r.ctx != context.Background() {
		if queryWithContext, ok := r.query.(contractsorm.QueryWithContext); ok {
			return queryWithContext.WithContext(r.ctx)
		}
	}

	return r.query
}

// NewRelation returns a Query pre-scoped to the related rows for parent.relation. parent must be
// a non-nil pointer to a struct. See contractsorm.Orm.NewRelation for the per-kind shape.
func (r *Orm) NewRelation(parent any, relation string) contractsorm.Query {
	q := r.Query()
	gq, ok := q.(*gorm.Query)
	if !ok {
		// Implementation invariant: r.query is always a *gorm.Query in this driver.
		// If a future driver implements contractsorm.Orm differently, that driver provides its
		// own NewRelation; this branch should never run in practice.
		_ = q
		return nil
	}
	return gq.NewRelation(parent, relation)
}

// Save inserts or updates child as a member of parent's relation. See contractsorm.Orm.Save.
func (r *Orm) Save(parent any, relation string, child any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.SaveRelation(parent, relation, child)
}

// SaveMany is the slice form of Save. See contractsorm.Orm.SaveMany.
func (r *Orm) SaveMany(parent any, relation string, children any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.SaveManyRelation(parent, relation, children)
}

// SaveWithPivot is Save with caller-supplied pivot column values for BelongsToMany relations.
// See contractsorm.Orm.SaveWithPivot.
func (r *Orm) SaveWithPivot(parent any, relation string, child any, attrs map[string]any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.SaveRelationWithPivot(parent, relation, child, attrs)
}

// SaveManyWithPivot is the slice form of SaveWithPivot. See contractsorm.Orm.SaveManyWithPivot.
func (r *Orm) SaveManyWithPivot(parent any, relation string, children any, attrsPerChild map[any]map[string]any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.SaveManyRelationWithPivot(parent, relation, children, attrsPerChild)
}

// Create persists a new related row. See contractsorm.Orm.Create.
func (r *Orm) Create(parent any, relation string, dest any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.CreateRelation(parent, relation, dest)
}

// CreateMany is the slice form of Create. See contractsorm.Orm.CreateMany.
func (r *Orm) CreateMany(parent any, relation string, dests any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.CreateManyRelation(parent, relation, dests)
}

// FindOrNew finds the related row with primary key id. See contractsorm.Orm.FindOrNew.
func (r *Orm) FindOrNew(parent any, relation string, id any, dest any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.FindOrNewRelation(parent, relation, id, dest)
}

// FirstOrNew finds the first related row matching attrs. See contractsorm.Orm.FirstOrNew.
func (r *Orm) FirstOrNew(parent any, relation string, attrs map[string]any, values map[string]any, dest any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.FirstOrNewRelation(parent, relation, attrs, values, dest)
}

// FirstOrCreate is FirstOrNew that persists when no matching row exists. See contractsorm.Orm.FirstOrCreate.
func (r *Orm) FirstOrCreate(parent any, relation string, attrs map[string]any, values map[string]any, dest any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.FirstOrCreateRelation(parent, relation, attrs, values, dest)
}

// UpdateOrCreate finds the first related row matching attrs (or creates one), then overlays values.
// See contractsorm.Orm.UpdateOrCreate.
func (r *Orm) UpdateOrCreate(parent any, relation string, attrs map[string]any, values map[string]any, dest any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.UpdateOrCreateRelation(parent, relation, attrs, values, dest)
}

// Associate sets parent's foreign key (and morph_type for MorphTo) to point at owner.
// See contractsorm.Orm.Associate.
func (r *Orm) Associate(parent any, relation string, owner any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.AssociateRelation(parent, relation, owner)
}

// Dissociate clears parent's foreign key (and morph_type for MorphTo) and persists parent.
// See contractsorm.Orm.Dissociate.
func (r *Orm) Dissociate(parent any, relation string) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.DissociateRelation(parent, relation)
}

// Attach inserts pivot rows linking parent to ids. See contractsorm.Orm.Attach.
func (r *Orm) Attach(parent any, relation string, ids []any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.AttachRelation(parent, relation, ids)
}

// AttachWithPivot is Attach with per-row pivot column values.
// See contractsorm.Orm.AttachWithPivot.
func (r *Orm) AttachWithPivot(parent any, relation string, idsWithAttrs map[any]map[string]any) error {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil
	}
	return gq.AttachWithPivotRelation(parent, relation, idsWithAttrs)
}

// Detach removes pivot rows linking parent to ids. With nil ids, removes all pivot rows for
// parent. See contractsorm.Orm.Detach.
func (r *Orm) Detach(parent any, relation string, ids ...any) (int64, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return 0, nil
	}
	return gq.DetachRelation(parent, relation, ids)
}

// Sync replaces parent's pivot rows so they exactly match ids. See contractsorm.Orm.Sync.
func (r *Orm) Sync(parent any, relation string, ids []any) (*dbcontract.SyncResult, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil, nil
	}
	return gq.SyncRelation(parent, relation, ids)
}

// SyncWithPivot is Sync with per-ID pivot column values. See contractsorm.Orm.SyncWithPivot.
func (r *Orm) SyncWithPivot(parent any, relation string, idsWithAttrs map[any]map[string]any) (*dbcontract.SyncResult, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil, nil
	}
	return gq.SyncRelationWithPivot(parent, relation, idsWithAttrs)
}

// SyncWithPivotValues applies the same pivot values to all ids. See contractsorm.Orm.SyncWithPivotValues.
func (r *Orm) SyncWithPivotValues(parent any, relation string, ids []any, pivotValues map[string]any) (*dbcontract.SyncResult, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil, nil
	}
	return gq.SyncRelationWithPivotValues(parent, relation, ids, pivotValues)
}

// SyncWithoutDetaching adds missing pivot rows only. See contractsorm.Orm.SyncWithoutDetaching.
func (r *Orm) SyncWithoutDetaching(parent any, relation string, ids []any) (*dbcontract.SyncResult, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil, nil
	}
	return gq.SyncWithoutDetachingRelation(parent, relation, ids)
}

// SyncWithoutDetachingWithPivot is SyncWithPivot minus the detach step. See contractsorm.Orm.SyncWithoutDetachingWithPivot.
func (r *Orm) SyncWithoutDetachingWithPivot(parent any, relation string, idsWithAttrs map[any]map[string]any) (*dbcontract.SyncResult, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil, nil
	}
	return gq.SyncWithoutDetachingRelationWithPivot(parent, relation, idsWithAttrs)
}

// Toggle attaches missing entries and detaches existing ones. See contractsorm.Orm.Toggle.
func (r *Orm) Toggle(parent any, relation string, ids []any) (*dbcontract.SyncResult, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil, nil
	}
	return gq.ToggleRelation(parent, relation, ids)
}

// ToggleWithPivot is Toggle with per-ID pivot column values. See contractsorm.Orm.ToggleWithPivot.
func (r *Orm) ToggleWithPivot(parent any, relation string, idsWithAttrs map[any]map[string]any) (*dbcontract.SyncResult, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return nil, nil
	}
	return gq.ToggleRelationWithPivot(parent, relation, idsWithAttrs)
}

// UpdateExistingPivot updates pivot columns for an already-attached id. See
// contractsorm.Orm.UpdateExistingPivot.
func (r *Orm) UpdateExistingPivot(parent any, relation string, id any, attrs map[string]any) (int64, error) {
	gq, ok := r.Query().(*gorm.Query)
	if !ok {
		return 0, nil
	}
	return gq.UpdateExistingPivotRelation(parent, relation, id, attrs)
}

func (r *Orm) SetQuery(query contractsorm.Query) {
	r.query = query
}

// TODO: The fresh logic needs to be optimized, it's a bit unclear now.
// https://github.com/goravel/goravel/issues/848
func (r *Orm) Fresh() {
	r.fresh(binding.Orm)
	driver.ResetConnections()
}

func (r *Orm) Transaction(txFunc func(tx contractsorm.Query) error) (err error) {
	tx, err := r.Query().Begin()
	if err != nil {
		return err
	}

	defer func() {
		if re := recover(); re != nil {
			err = fmt.Errorf("panic: %v", re)
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
		}
	}()

	if err := txFunc(tx); err != nil {
		if err := tx.Rollback(); err != nil {
			return err
		}

		return err
	} else {
		return tx.Commit()
	}
}

func (r *Orm) WithContext(ctx context.Context) contractsorm.Orm {
	return NewOrm(ctx, r.config, r.connection, r.dbConfig, r.query, r.queries, r.log, r.modelToObserver, r.fresh)
}

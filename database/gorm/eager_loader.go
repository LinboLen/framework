package gorm

import (
	"fmt"
	"reflect"
	"strings"

	gormio "gorm.io/gorm"
	gormschema "gorm.io/gorm/schema"

	"github.com/goravel/framework/errors"
)

// applyEagerLoads runs all queued WithRelation entries against the just-loaded dest. It must be
// called by terminal methods (Get / Find / First / FirstOrFail / FirstOr / Cursor) after the main
// query has populated dest, and only when conditions.eagerLoad is non-empty.
func (r *Query) applyEagerLoads(dest any) error {
	if len(r.conditions.eagerLoad) == 0 {
		return nil
	}
	parents, err := collectEagerParents(dest)
	if err != nil {
		return err
	}
	entries := r.conditions.eagerLoad
	r.conditions.eagerLoad = nil
	if len(parents) == 0 {
		return nil
	}
	return r.runEagerLoads(parents, entries)
}

// runEagerLoads iterates the eager-load entries and dispatches each top-level relation to its
// kind-specific loader. Nested entries (those whose name contains a dot) are handled by the
// trickle-down recursion inside each loader, mirroring fedaco's eagerLoadRelations.
func (r *Query) runEagerLoads(parents []reflect.Value, entries []eagerLoadEntry) error {
	if len(parents) == 0 || len(entries) == 0 {
		return nil
	}
	parentModel := newSampleModel(parents[0])
	for _, entry := range entries {
		if strings.Contains(entry.relation, ".") {
			continue
		}
		nested := directNestedEntries(entries, entry.relation)
		if err := r.loadOneRelation(parents, parentModel, entry, nested); err != nil {
			return err
		}
	}
	return nil
}

func (r *Query) loadOneRelation(parents []reflect.Value, parentModel any, entry eagerLoadEntry, nested []eagerLoadEntry) error {
	desc, err := resolveRelation(r.instance, parentModel, entry.relation)
	if err != nil {
		return err
	}
	switch desc.kind {
	case relKindHasOne, relKindHasMany:
		return r.loadHasOneOrMany(parents, parentModel, desc, entry, nested, desc.kind == relKindHasMany)
	case relKindBelongsTo:
		return r.loadBelongsTo(parents, parentModel, desc, entry, nested)
	case relKindMany2Many:
		return r.loadMany2Many(parents, parentModel, desc, entry, nested)
	case relKindMorphOne, relKindMorphMany:
		return r.loadMorph(parents, parentModel, desc, entry, nested, desc.kind == relKindMorphMany)
	case relKindHasOneThrough, relKindHasManyThrough:
		return r.loadThrough(parents, parentModel, desc, entry, nested, desc.kind == relKindHasManyThrough)
	}
	return errors.OrmRelationUnsupported.Args(entry.relation, reflect.TypeOf(parentModel).String(), fmt.Sprintf("kind=%d", desc.kind))
}

// ---------------------------------------------------------------------------
// Per-kind loaders
// ---------------------------------------------------------------------------

func (r *Query) loadHasOneOrMany(parents []reflect.Value, parentModel any, desc *relationDescriptor, entry eagerLoadEntry, nested []eagerLoadEntry, isMany bool) error {
	if len(desc.references) == 0 {
		return errors.OrmRelationUnsupported.Args(entry.relation, "", "no references")
	}
	ref := desc.references[0]
	parentSchema, err := parseGormSchema(r.instance, parentModel)
	if err != nil {
		return err
	}
	parentField, ok := parentSchema.FieldsByDBName[ref.primaryColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, parentSchema.Name, "no parent field for "+ref.primaryColumn)
	}

	keys := extractKeys(r, parents, parentField)
	if len(keys) == 0 {
		return r.maybeRecurseEmpty(parents, entry.relation, isMany, nested)
	}

	inner := r.freshSession().Table(desc.relatedTable).Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.relatedTable), quoteIdent(ref.foreignColumn)), keys)
	rows, err := r.runRelatedQuery(inner, desc, entry, []string{ref.foreignColumn})
	if err != nil {
		return err
	}

	relatedSchema, err := parseGormSchema(r.instance, desc.relatedModel)
	if err != nil {
		return err
	}
	fkField, ok := relatedSchema.FieldsByDBName[ref.foreignColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, relatedSchema.Name, "no related FK field for "+ref.foreignColumn)
	}

	dict := make(map[string][]reflect.Value, len(rows))
	for _, row := range rows {
		val, _ := fkField.ValueOf(r.ctx, row.Elem())
		dict[dictKey(val)] = append(dict[dictKey(val)], row)
	}

	if err := r.assignToParents(parents, parentField, entry.relation, dict, isMany); err != nil {
		return err
	}
	return r.recurseNested(rows, nested)
}

func (r *Query) loadBelongsTo(parents []reflect.Value, parentModel any, desc *relationDescriptor, entry eagerLoadEntry, nested []eagerLoadEntry) error {
	if len(desc.references) == 0 {
		return errors.OrmRelationUnsupported.Args(entry.relation, "", "no references")
	}
	ref := desc.references[0]
	parentSchema, err := parseGormSchema(r.instance, parentModel)
	if err != nil {
		return err
	}
	// For BelongsTo: ref.foreignTable=parent, ref.foreignColumn=FK on parent;
	// ref.primaryTable=related, ref.primaryColumn=PK on related.
	fkField, ok := parentSchema.FieldsByDBName[ref.foreignColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, parentSchema.Name, "no parent FK field for "+ref.foreignColumn)
	}

	keys := extractKeys(r, parents, fkField)
	if len(keys) == 0 {
		return r.maybeRecurseEmpty(parents, entry.relation, false, nested)
	}

	inner := r.freshSession().Table(desc.relatedTable).Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.relatedTable), quoteIdent(ref.primaryColumn)), keys)
	rows, err := r.runRelatedQuery(inner, desc, entry, []string{ref.primaryColumn})
	if err != nil {
		return err
	}

	relatedSchema, err := parseGormSchema(r.instance, desc.relatedModel)
	if err != nil {
		return err
	}
	pkField, ok := relatedSchema.FieldsByDBName[ref.primaryColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, relatedSchema.Name, "no related PK field for "+ref.primaryColumn)
	}

	dict := make(map[string][]reflect.Value, len(rows))
	for _, row := range rows {
		val, _ := pkField.ValueOf(r.ctx, row.Elem())
		dict[dictKey(val)] = append(dict[dictKey(val)], row)
	}

	if err := r.assignToParents(parents, fkField, entry.relation, dict, false); err != nil {
		return err
	}
	return r.recurseNested(rows, nested)
}

func (r *Query) loadMorph(parents []reflect.Value, parentModel any, desc *relationDescriptor, entry eagerLoadEntry, nested []eagerLoadEntry, isMany bool) error {
	if len(desc.references) == 0 {
		return errors.OrmRelationUnsupported.Args(entry.relation, "", "no references")
	}
	ref := desc.references[0]
	parentSchema, err := parseGormSchema(r.instance, parentModel)
	if err != nil {
		return err
	}
	parentField, ok := parentSchema.FieldsByDBName[ref.primaryColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, parentSchema.Name, "no parent field for "+ref.primaryColumn)
	}

	keys := extractKeys(r, parents, parentField)
	if len(keys) == 0 {
		return r.maybeRecurseEmpty(parents, entry.relation, isMany, nested)
	}

	inner := r.freshSession().
		Table(desc.relatedTable).
		Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.relatedTable), quoteIdent(ref.foreignColumn)), keys).
		Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.relatedTable), quoteIdent(desc.morphTypeColumn)), desc.morphValue)
	rows, err := r.runRelatedQuery(inner, desc, entry, []string{ref.foreignColumn, desc.morphTypeColumn})
	if err != nil {
		return err
	}

	relatedSchema, err := parseGormSchema(r.instance, desc.relatedModel)
	if err != nil {
		return err
	}
	fkField, ok := relatedSchema.FieldsByDBName[ref.foreignColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, relatedSchema.Name, "no related FK field for "+ref.foreignColumn)
	}

	dict := make(map[string][]reflect.Value, len(rows))
	for _, row := range rows {
		val, _ := fkField.ValueOf(r.ctx, row.Elem())
		dict[dictKey(val)] = append(dict[dictKey(val)], row)
	}

	if err := r.assignToParents(parents, parentField, entry.relation, dict, isMany); err != nil {
		return err
	}
	return r.recurseNested(rows, nested)
}

func (r *Query) loadMany2Many(parents []reflect.Value, parentModel any, desc *relationDescriptor, entry eagerLoadEntry, nested []eagerLoadEntry) error {
	parentSchema, err := parseGormSchema(r.instance, parentModel)
	if err != nil {
		return err
	}
	parentField, ok := parentSchema.FieldsByDBName[desc.pivotParentRef.primaryColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, parentSchema.Name, "no parent field for "+desc.pivotParentRef.primaryColumn)
	}

	keys := extractKeys(r, parents, parentField)
	if len(keys) == 0 {
		return r.maybeRecurseEmpty(parents, entry.relation, true, nested)
	}

	pivotParentCol := desc.pivotParentRef.foreignColumn
	pivotRelatedCol := desc.pivotRelatedRef.foreignColumn

	var pivotRows []map[string]any
	if err := r.freshSession().
		Table(desc.pivotTable).
		Select(pivotParentCol, pivotRelatedCol).
		Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.pivotTable), quoteIdent(pivotParentCol)), keys).
		Find(&pivotRows).Error; err != nil {
		return err
	}
	if len(pivotRows) == 0 {
		return r.maybeRecurseEmpty(parents, entry.relation, true, nested)
	}

	relatedKeysSet := make(map[string]any, len(pivotRows))
	for _, p := range pivotRows {
		k := dictKey(p[pivotRelatedCol])
		if _, exists := relatedKeysSet[k]; !exists {
			relatedKeysSet[k] = p[pivotRelatedCol]
		}
	}
	relatedKeys := make([]any, 0, len(relatedKeysSet))
	for _, v := range relatedKeysSet {
		relatedKeys = append(relatedKeys, v)
	}

	inner := r.freshSession().
		Table(desc.relatedTable).
		Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.relatedTable), quoteIdent(desc.pivotRelatedRef.primaryColumn)), relatedKeys)
	rows, err := r.runRelatedQuery(inner, desc, entry, []string{desc.pivotRelatedRef.primaryColumn})
	if err != nil {
		return err
	}

	relatedSchema, err := parseGormSchema(r.instance, desc.relatedModel)
	if err != nil {
		return err
	}
	relatedPKField, ok := relatedSchema.FieldsByDBName[desc.pivotRelatedRef.primaryColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, relatedSchema.Name, "no related PK field for "+desc.pivotRelatedRef.primaryColumn)
	}
	relatedByID := make(map[string]reflect.Value, len(rows))
	for _, row := range rows {
		val, _ := relatedPKField.ValueOf(r.ctx, row.Elem())
		relatedByID[dictKey(val)] = row
	}

	dict := make(map[string][]reflect.Value, len(parents))
	for _, p := range pivotRows {
		parentKey := dictKey(p[pivotParentCol])
		relatedKey := dictKey(p[pivotRelatedCol])
		if rel, ok := relatedByID[relatedKey]; ok {
			dict[parentKey] = append(dict[parentKey], rel)
		}
	}

	if err := r.assignToParents(parents, parentField, entry.relation, dict, true); err != nil {
		return err
	}
	return r.recurseNested(rows, nested)
}

func (r *Query) loadThrough(parents []reflect.Value, parentModel any, desc *relationDescriptor, entry eagerLoadEntry, nested []eagerLoadEntry, isMany bool) error {
	parentSchema, err := parseGormSchema(r.instance, parentModel)
	if err != nil {
		return err
	}
	parentField, ok := parentSchema.FieldsByDBName[desc.localKey]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, parentSchema.Name, "no parent field for "+desc.localKey)
	}

	keys := extractKeys(r, parents, parentField)
	if len(keys) == 0 {
		return r.maybeRecurseEmpty(parents, entry.relation, isMany, nested)
	}

	var throughRows []map[string]any
	if err := r.freshSession().
		Table(desc.throughTable).
		Select(desc.firstKey, desc.secondLocalKey).
		Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.throughTable), quoteIdent(desc.firstKey)), keys).
		Find(&throughRows).Error; err != nil {
		return err
	}
	if len(throughRows) == 0 {
		return r.maybeRecurseEmpty(parents, entry.relation, isMany, nested)
	}

	secondKeysSet := make(map[string]any, len(throughRows))
	for _, t := range throughRows {
		k := dictKey(t[desc.secondLocalKey])
		if _, exists := secondKeysSet[k]; !exists {
			secondKeysSet[k] = t[desc.secondLocalKey]
		}
	}
	secondKeys := make([]any, 0, len(secondKeysSet))
	for _, v := range secondKeysSet {
		secondKeys = append(secondKeys, v)
	}

	inner := r.freshSession().
		Table(desc.relatedTable).
		Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.relatedTable), quoteIdent(desc.secondKey)), secondKeys)
	rows, err := r.runRelatedQuery(inner, desc, entry, []string{desc.secondKey})
	if err != nil {
		return err
	}

	relatedSchema, err := parseGormSchema(r.instance, desc.relatedModel)
	if err != nil {
		return err
	}
	secondField, ok := relatedSchema.FieldsByDBName[desc.secondKey]
	if !ok {
		return errors.OrmRelationUnsupported.Args(entry.relation, relatedSchema.Name, "no related field for "+desc.secondKey)
	}
	relatedByThrough := make(map[string][]reflect.Value, len(rows))
	for _, row := range rows {
		val, _ := secondField.ValueOf(r.ctx, row.Elem())
		k := dictKey(val)
		relatedByThrough[k] = append(relatedByThrough[k], row)
	}

	dict := make(map[string][]reflect.Value, len(parents))
	for _, t := range throughRows {
		parentKey := dictKey(t[desc.firstKey])
		secondKey := dictKey(t[desc.secondLocalKey])
		if rels, ok := relatedByThrough[secondKey]; ok {
			dict[parentKey] = append(dict[parentKey], rels...)
		}
	}

	if err := r.assignToParents(parents, parentField, entry.relation, dict, isMany); err != nil {
		return err
	}
	return r.recurseNested(rows, nested)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// runRelatedQuery applies the user's callback and column pruning to the inner builder, executes
// it, and returns the result rows as []reflect.Value where each value is a *RelatedModel.
//
// requiredCols are columns the loader needs back (FK columns, PK columns) to build dictionaries;
// they are appended to the user's prune list when not already present so the caller does not have
// to remember to include them.
func (r *Query) runRelatedQuery(inner *gormio.DB, desc *relationDescriptor, entry eagerLoadEntry, requiredCols []string) ([]reflect.Value, error) {
	if entry.callback != nil {
		wrapper := r.wrap(inner)
		wrapped := entry.callback(wrapper)
		if w, ok := wrapped.(*Query); ok {
			inner = w.buildConditions().instance
		}
	}
	if len(entry.columns) > 0 {
		cols := append([]string(nil), entry.columns...)
		for _, req := range requiredCols {
			if !containsCol(cols, req) {
				cols = append(cols, req)
			}
		}
		inner = inner.Select(cols)
	}

	relatedType := reflect.TypeOf(desc.relatedModel)
	if relatedType.Kind() == reflect.Pointer {
		relatedType = relatedType.Elem()
	}
	sliceType := reflect.SliceOf(reflect.PointerTo(relatedType))
	slicePtr := reflect.New(sliceType)
	if err := inner.Find(slicePtr.Interface()).Error; err != nil {
		return nil, err
	}
	slice := slicePtr.Elem()
	out := make([]reflect.Value, 0, slice.Len())
	for i := 0; i < slice.Len(); i++ {
		out = append(out, slice.Index(i))
	}
	return out, nil
}

// assignToParents writes the dictionary entries onto each parent's relation field using
// setRelationField. When isMany is false and a parent has multiple matches, only the first one is
// assigned (HasOne / BelongsTo / MorphOne / HasOneThrough cases).
func (r *Query) assignToParents(parents []reflect.Value, parentField *gormschema.Field, relation string, dict map[string][]reflect.Value, isMany bool) error {
	for _, parent := range parents {
		val, zero := parentField.ValueOf(r.ctx, parent)
		if zero {
			if !isMany {
				continue
			}
			if err := setRelationField(parent, relation, nil); err != nil {
				return err
			}
			continue
		}
		match := dict[dictKey(val)]
		if !isMany && len(match) > 1 {
			match = match[:1]
		}
		if err := setRelationField(parent, relation, match); err != nil {
			return err
		}
	}
	return nil
}

func (r *Query) recurseNested(rows []reflect.Value, nested []eagerLoadEntry) error {
	if len(rows) == 0 || len(nested) == 0 {
		return nil
	}
	nestedParents := make([]reflect.Value, 0, len(rows))
	for _, row := range rows {
		nestedParents = append(nestedParents, row.Elem())
	}
	return r.runEagerLoads(nestedParents, nested)
}

// maybeRecurseEmpty is the no-op fast path: when there are no parent keys to load against, leave
// each parent's relation field at its zero value (or empty slice for many) and skip nested.
func (r *Query) maybeRecurseEmpty(parents []reflect.Value, relation string, isMany bool, _ []eagerLoadEntry) error {
	if !isMany {
		return nil
	}
	for _, parent := range parents {
		if err := setRelationField(parent, relation, nil); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Reflect / extraction helpers
// ---------------------------------------------------------------------------

// collectEagerParents extracts the addressable struct values from dest. dest may be *Struct,
// *[]Struct, or *[]*Struct; each form yields a flat slice of struct values whose fields can be
// mutated.
func collectEagerParents(dest any) ([]reflect.Value, error) {
	if dest == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, nil
	}
	elem := rv.Elem()
	switch elem.Kind() {
	case reflect.Struct:
		return []reflect.Value{elem}, nil
	case reflect.Slice:
		out := make([]reflect.Value, 0, elem.Len())
		for i := 0; i < elem.Len(); i++ {
			item := elem.Index(i)
			if item.Kind() == reflect.Pointer {
				if item.IsNil() {
					continue
				}
				item = item.Elem()
			}
			if item.Kind() != reflect.Struct {
				continue
			}
			out = append(out, item)
		}
		return out, nil
	}
	return nil, nil
}

// newSampleModel returns a fresh pointer-to-struct of the same type as parent. resolveRelation
// expects an addressable model instance (it parses the schema and inspects its tags), and we
// don't want to hand it one of our actual parent rows (which may carry mutated fields).
func newSampleModel(parent reflect.Value) any {
	t := parent.Type()
	return reflect.New(t).Interface()
}

func parseGormSchema(db *gormio.DB, model any) (*gormschema.Schema, error) {
	stmt := &gormio.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return nil, err
	}
	return stmt.Schema, nil
}

// extractKeys pulls the unique non-zero values of field from the parent slice.
func extractKeys(r *Query, parents []reflect.Value, field *gormschema.Field) []any {
	seen := make(map[string]struct{}, len(parents))
	out := make([]any, 0, len(parents))
	for _, parent := range parents {
		val, zero := field.ValueOf(r.ctx, parent)
		if zero {
			continue
		}
		k := dictKey(val)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, val)
	}
	return out
}

// dictKey reduces any value to a canonical string for use as a map key, paving over the type
// mismatch between Go field types (uint, int64, string) and database-layer scan types
// (often int64 or []byte). Mirrors fedaco's _getDictionaryKey.
func dictKey(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// containsCol checks whether col (or "<table>.col") already appears in cols.
func containsCol(cols []string, col string) bool {
	for _, c := range cols {
		if c == col {
			return true
		}
		if _, suffix, ok := strings.Cut(c, "."); ok && suffix == col {
			return true
		}
	}
	return false
}

// setRelationField writes loaded rows back to parent's relation field. Supports *Model,
// []*Model and []Model field shapes.
func setRelationField(parent reflect.Value, fieldName string, rows []reflect.Value) error {
	field := parent.FieldByName(fieldName)
	if !field.IsValid() {
		return errors.OrmEagerLoadCannotAssign.Args(fieldName, parent.Type().String())
	}
	if !field.CanSet() {
		return errors.OrmEagerLoadCannotAssign.Args(fieldName, parent.Type().String())
	}

	switch field.Kind() {
	case reflect.Pointer:
		if len(rows) == 0 {
			field.Set(reflect.Zero(field.Type()))
			return nil
		}
		row := rows[0]
		if row.Type() == field.Type() {
			field.Set(row)
			return nil
		}
		if row.Kind() == reflect.Pointer && row.Type().Elem() == field.Type().Elem() {
			field.Set(row)
			return nil
		}
		return errors.OrmEagerLoadCannotAssign.Args(fieldName, parent.Type().String())

	case reflect.Slice:
		elemType := field.Type().Elem()
		out := reflect.MakeSlice(field.Type(), 0, len(rows))
		for _, row := range rows {
			switch elemType.Kind() {
			case reflect.Pointer:
				if row.Type() == elemType {
					out = reflect.Append(out, row)
					continue
				}
				if row.Kind() == reflect.Pointer && row.Type().Elem() == elemType.Elem() {
					out = reflect.Append(out, row)
					continue
				}
				return errors.OrmEagerLoadCannotAssign.Args(fieldName, parent.Type().String())
			case reflect.Struct:
				if row.Kind() == reflect.Pointer && row.Type().Elem() == elemType {
					out = reflect.Append(out, row.Elem())
					continue
				}
				if row.Type() == elemType {
					out = reflect.Append(out, row)
					continue
				}
				return errors.OrmEagerLoadCannotAssign.Args(fieldName, parent.Type().String())
			default:
				return errors.OrmEagerLoadCannotAssign.Args(fieldName, parent.Type().String())
			}
		}
		field.Set(out)
		return nil
	}
	return errors.OrmEagerLoadCannotAssign.Args(fieldName, parent.Type().String())
}

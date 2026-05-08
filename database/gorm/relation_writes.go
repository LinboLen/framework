package gorm

import (
	"fmt"
	"reflect"

	dbcontract "github.com/goravel/framework/contracts/database/db"
	"github.com/goravel/framework/errors"
)

// SaveRelation inserts or updates child as a member of parent's relation. Sets child's foreign
// key (and morph_type for MorphOne/MorphMany) from parent's local key, then persists child via
// Query.Save.
//
// Public Query-level helper used by Orm.Save. Named "SaveRelation" to avoid clashing with the
// existing single-arg Query.Save(value any) which persists a model directly.
//
// Supported kinds: HasOne, HasMany, MorphOne, MorphMany. Other kinds error with
// OrmRelationKindNotSupported.
func (r *Query) SaveRelation(parent any, relation string, child any) error {
	if !isValidParent(parent) {
		return errors.OrmNewRelationParentNotPointer.Args(parent)
	}
	if !isValidParent(child) {
		return errors.OrmNewRelationParentNotPointer.Args(child)
	}
	desc, err := resolveRelation(r.instance, parent, relation)
	if err != nil {
		return err
	}
	switch desc.kind {
	case relKindHasOne, relKindHasMany, relKindMorphOne, relKindMorphMany:
		if err := r.setRelationFKOnChild(parent, child, desc); err != nil {
			return err
		}
		return r.wrap(r.freshSession()).Save(child)
	default:
		return errors.OrmRelationKindNotSupported.Args("Save", relation, kindName(desc.kind))
	}
}

// SaveManyRelation is the slice form of SaveRelation. children must be a slice or pointer-to-
// slice of either pointer-to-struct or struct elements. Iterates and bails on first error.
func (r *Query) SaveManyRelation(parent any, relation string, children any) error {
	rv := reflect.ValueOf(children)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice {
		return errors.OrmRelationKindNotSupported.Args("SaveMany", relation, fmt.Sprintf("children=%T (must be slice)", children))
	}
	for i := 0; i < rv.Len(); i++ {
		item := rv.Index(i)
		var elem any
		switch item.Kind() {
		case reflect.Pointer:
			elem = item.Interface()
		case reflect.Struct:
			if !item.CanAddr() {
				ptr := reflect.New(item.Type())
				ptr.Elem().Set(item)
				elem = ptr.Interface()
			} else {
				elem = item.Addr().Interface()
			}
		default:
			return errors.OrmRelationKindNotSupported.Args("SaveMany", relation, fmt.Sprintf("children element=%s", item.Kind()))
		}
		if err := r.SaveRelation(parent, relation, elem); err != nil {
			return err
		}
	}
	return nil
}

// AssociateRelation sets parent's foreign key (and morph_type for MorphTo) to point at owner,
// then persists parent. Supported kinds: BelongsTo, MorphTo. owner must be a non-nil pointer to
// a struct.
//
// Public Query-level helper used by Orm.Associate.
func (r *Query) AssociateRelation(parent any, relation string, owner any) error {
	if !isValidParent(parent) {
		return errors.OrmNewRelationParentNotPointer.Args(parent)
	}
	if !isValidParent(owner) {
		return errors.OrmNewRelationParentNotPointer.Args(owner)
	}
	desc, err := resolveRelation(r.instance, parent, relation)
	if err != nil {
		return err
	}
	switch desc.kind {
	case relKindBelongsTo:
		return r.applyAssociate(parent, owner, desc, false)
	case relKindMorphTo:
		return r.applyAssociate(parent, owner, desc, true)
	default:
		return errors.OrmRelationKindNotSupported.Args("Associate", relation, kindName(desc.kind))
	}
}

// DissociateRelation clears parent's foreign key (and morph_type for MorphTo) and persists
// parent. Supported kinds: BelongsTo, MorphTo.
func (r *Query) DissociateRelation(parent any, relation string) error {
	if !isValidParent(parent) {
		return errors.OrmNewRelationParentNotPointer.Args(parent)
	}
	desc, err := resolveRelation(r.instance, parent, relation)
	if err != nil {
		return err
	}
	switch desc.kind {
	case relKindBelongsTo:
		return r.applyDissociate(parent, desc, false)
	case relKindMorphTo:
		return r.applyDissociate(parent, desc, true)
	default:
		return errors.OrmRelationKindNotSupported.Args("Dissociate", relation, kindName(desc.kind))
	}
}

// applyAssociate writes owner's PK into parent's FK column (and the morph_type column for
// MorphTo, resolved from the morph map / MorphClass()), then persists parent.
func (r *Query) applyAssociate(parent, owner any, desc *relationDescriptor, isMorph bool) error {
	if err := r.mutateAssociate(parent, owner, desc, isMorph); err != nil {
		return err
	}
	return r.wrap(r.freshSession()).Save(parent)
}

// mutateAssociate is the pure-mutation half of applyAssociate. Writes owner's PK into parent's
// FK column (and the morph_type column for MorphTo). No persistence.
func (r *Query) mutateAssociate(parent, owner any, desc *relationDescriptor, isMorph bool) error {
	parentSchema, err := parseGormSchema(r.instance, parent)
	if err != nil {
		return err
	}
	parentRV := reflect.ValueOf(parent).Elem()

	var fkColumn string
	if isMorph {
		fkColumn = desc.morphIDColumn
	} else {
		if len(desc.references) == 0 {
			return errors.OrmRelationUnsupported.Args(desc.name, desc.parentTable, "no references")
		}
		fkColumn = desc.references[0].foreignColumn
	}
	fkField, ok := parentSchema.FieldsByDBName[fkColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(desc.name, parentSchema.Name, "no FK field "+fkColumn)
	}

	ownerPKColumn := "id"
	if !isMorph && len(desc.references) > 0 {
		ownerPKColumn = desc.references[0].primaryColumn
	} else if isMorph && desc.morphOwnerKey != "" {
		ownerPKColumn = desc.morphOwnerKey
	}
	ownerPK, err := readParentColumn(r, owner, ownerPKColumn)
	if err != nil {
		return err
	}
	if err := fkField.Set(r.ctx, parentRV, ownerPK); err != nil {
		return err
	}

	if isMorph {
		typeField, ok := parentSchema.FieldsByDBName[desc.morphTypeColumn]
		if !ok {
			return errors.OrmRelationUnsupported.Args(desc.name, parentSchema.Name, "no morph type field "+desc.morphTypeColumn)
		}
		alias, ok := resolveMorphAlias(owner)
		if !ok {
			tbl, terr := tableNameFor(r.instance, owner)
			if terr != nil {
				return terr
			}
			alias = tbl
		}
		if err := typeField.Set(r.ctx, parentRV, alias); err != nil {
			return err
		}
	}
	return nil
}

// applyDissociate sets parent's FK to the zero value (and morph_type to "" for MorphTo), then
// persists parent.
func (r *Query) applyDissociate(parent any, desc *relationDescriptor, isMorph bool) error {
	if err := r.mutateDissociate(parent, desc, isMorph); err != nil {
		return err
	}
	return r.wrap(r.freshSession()).Save(parent)
}

// mutateDissociate is the pure-mutation half of applyDissociate.
func (r *Query) mutateDissociate(parent any, desc *relationDescriptor, isMorph bool) error {
	parentSchema, err := parseGormSchema(r.instance, parent)
	if err != nil {
		return err
	}
	parentRV := reflect.ValueOf(parent).Elem()

	var fkColumn string
	if isMorph {
		fkColumn = desc.morphIDColumn
	} else {
		if len(desc.references) == 0 {
			return errors.OrmRelationUnsupported.Args(desc.name, desc.parentTable, "no references")
		}
		fkColumn = desc.references[0].foreignColumn
	}
	fkField, ok := parentSchema.FieldsByDBName[fkColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(desc.name, parentSchema.Name, "no FK field "+fkColumn)
	}
	zero := reflect.Zero(fkField.FieldType).Interface()
	if err := fkField.Set(r.ctx, parentRV, zero); err != nil {
		return err
	}

	if isMorph {
		typeField, ok := parentSchema.FieldsByDBName[desc.morphTypeColumn]
		if !ok {
			return errors.OrmRelationUnsupported.Args(desc.name, parentSchema.Name, "no morph type field "+desc.morphTypeColumn)
		}
		zeroType := reflect.Zero(typeField.FieldType).Interface()
		if err := typeField.Set(r.ctx, parentRV, zeroType); err != nil {
			return err
		}
	}
	return nil
}

// AttachRelation inserts pivot rows linking parent to each id in ids. Skips ids that already
// have a pivot row. Supported kinds: Many2Many, MorphToMany, MorphedByMany.
//
// Public Query-level helper used by Orm.Attach.
func (r *Query) AttachRelation(parent any, relation string, ids []any) error {
	desc, parentVal, err := r.resolvePivot(parent, relation, "Attach")
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	existing, err := r.existingPivotIDs(desc, parentVal, ids)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if _, dup := existing[dictKey(id)]; dup {
			continue
		}
		rows = append(rows, r.basePivotRow(desc, parentVal, id, nil))
	}
	if len(rows) == 0 {
		return nil
	}
	return r.freshSession().Table(desc.pivotTable).Create(rows).Error
}

// AttachWithPivotRelation is Attach with per-row pivot column values.
func (r *Query) AttachWithPivotRelation(parent any, relation string, idsWithAttrs map[any]map[string]any) error {
	desc, parentVal, err := r.resolvePivot(parent, relation, "AttachWithPivot")
	if err != nil {
		return err
	}
	if len(idsWithAttrs) == 0 {
		return nil
	}
	ids := make([]any, 0, len(idsWithAttrs))
	for id := range idsWithAttrs {
		ids = append(ids, id)
	}
	existing, err := r.existingPivotIDs(desc, parentVal, ids)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, 0, len(ids))
	for id, attrs := range idsWithAttrs {
		if _, dup := existing[dictKey(id)]; dup {
			continue
		}
		rows = append(rows, r.basePivotRow(desc, parentVal, id, attrs))
	}
	if len(rows) == 0 {
		return nil
	}
	return r.freshSession().Table(desc.pivotTable).Create(rows).Error
}

// DetachRelation removes pivot rows linking parent to the given ids. With nil/empty ids, removes
// all pivot rows for parent (and morph type, for polymorphic). Returns the number of rows
// removed.
func (r *Query) DetachRelation(parent any, relation string, ids []any) (int64, error) {
	desc, parentVal, err := r.resolvePivot(parent, relation, "Detach")
	if err != nil {
		return 0, err
	}
	q := r.freshSession().Table(desc.pivotTable).
		Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.pivotParentRef.foreignColumn)), parentVal)
	if desc.kind == relKindMorphToMany {
		q = q.Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.morphTypeColumn)), desc.morphValue)
	}
	if len(ids) > 0 {
		q = q.Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.pivotTable), quoteIdent(desc.pivotRelatedRef.foreignColumn)), ids)
	}
	res := q.Delete(nil)
	return res.RowsAffected, res.Error
}

// resolvePivot is the shared front-half for all pivot operations: validates parent, resolves the
// descriptor, asserts a pivot-friendly kind, and reads the parent's PK that anchors every pivot
// row. Returns the descriptor + parent's PK value.
func (r *Query) resolvePivot(parent any, relation, op string) (*relationDescriptor, any, error) {
	if !isValidParent(parent) {
		return nil, nil, errors.OrmNewRelationParentNotPointer.Args(parent)
	}
	desc, err := resolveRelation(r.instance, parent, relation)
	if err != nil {
		return nil, nil, err
	}
	if desc.kind != relKindMany2Many && desc.kind != relKindMorphToMany {
		return nil, nil, errors.OrmRelationKindNotSupported.Args(op, relation, kindName(desc.kind))
	}
	parentVal, err := readParentColumn(r, parent, desc.pivotParentRef.primaryColumn)
	if err != nil {
		return nil, nil, err
	}
	return desc, parentVal, nil
}

// basePivotRow builds the column map for one pivot INSERT row. Always includes the parent FK and
// the related FK; for MorphToMany also includes the morph_type column. Caller-supplied attrs are
// merged on top — the caller wins on column-name conflicts.
func (r *Query) basePivotRow(desc *relationDescriptor, parentVal, relatedID any, attrs map[string]any) map[string]any {
	row := map[string]any{
		desc.pivotParentRef.foreignColumn:  parentVal,
		desc.pivotRelatedRef.foreignColumn: relatedID,
	}
	if desc.kind == relKindMorphToMany {
		row[desc.morphTypeColumn] = desc.morphValue
	}
	for k, v := range attrs {
		row[k] = v
	}
	return row
}

// existingPivotIDs returns the set of already-attached related ids among ids. Used by Attach to
// skip duplicates.
func (r *Query) existingPivotIDs(desc *relationDescriptor, parentVal any, ids []any) (map[string]struct{}, error) {
	q := r.freshSession().
		Table(desc.pivotTable).
		Select(desc.pivotRelatedRef.foreignColumn).
		Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.pivotParentRef.foreignColumn)), parentVal).
		Where(fmt.Sprintf("%s.%s IN ?", quoteIdent(desc.pivotTable), quoteIdent(desc.pivotRelatedRef.foreignColumn)), ids)
	if desc.kind == relKindMorphToMany {
		q = q.Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.morphTypeColumn)), desc.morphValue)
	}
	var rows []map[string]any
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		out[dictKey(row[desc.pivotRelatedRef.foreignColumn])] = struct{}{}
	}
	return out, nil
}

// allPivotIDs returns the set of all currently-attached related ids for parent. Used by Sync /
// Toggle to compute the diff.
func (r *Query) allPivotIDs(desc *relationDescriptor, parentVal any) ([]any, error) {
	q := r.freshSession().
		Table(desc.pivotTable).
		Select(desc.pivotRelatedRef.foreignColumn).
		Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.pivotParentRef.foreignColumn)), parentVal)
	if desc.kind == relKindMorphToMany {
		q = q.Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.morphTypeColumn)), desc.morphValue)
	}
	var rows []map[string]any
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row[desc.pivotRelatedRef.foreignColumn])
	}
	return out, nil
}

// SyncRelation replaces parent's pivot rows so they exactly match ids: detaches missing entries,
// attaches new ones, leaves existing untouched. Returns the per-id outcome.
//
// Public Query-level helper used by Orm.Sync.
func (r *Query) SyncRelation(parent any, relation string, ids []any) (*dbcontract.SyncResult, error) {
	return r.syncCore(parent, relation, ids, true /*detach*/, false /*toggle*/, "Sync")
}

// SyncWithoutDetachingRelation is SyncRelation minus the detach step.
func (r *Query) SyncWithoutDetachingRelation(parent any, relation string, ids []any) (*dbcontract.SyncResult, error) {
	return r.syncCore(parent, relation, ids, false /*detach*/, false /*toggle*/, "SyncWithoutDetaching")
}

// ToggleRelation attaches missing entries and detaches existing ones.
func (r *Query) ToggleRelation(parent any, relation string, ids []any) (*dbcontract.SyncResult, error) {
	return r.syncCore(parent, relation, ids, false, true /*toggle*/, "Toggle")
}

// syncCore is the shared engine for Sync / SyncWithoutDetaching / Toggle.
func (r *Query) syncCore(parent any, relation string, ids []any, detachMissing bool, toggle bool, op string) (*dbcontract.SyncResult, error) {
	desc, parentVal, err := r.resolvePivot(parent, relation, op)
	if err != nil {
		return nil, err
	}
	current, err := r.allPivotIDs(desc, parentVal)
	if err != nil {
		return nil, err
	}
	currentSet := make(map[string]any, len(current))
	for _, id := range current {
		currentSet[dictKey(id)] = id
	}
	wantSet := make(map[string]any, len(ids))
	for _, id := range ids {
		wantSet[dictKey(id)] = id
	}

	out := &dbcontract.SyncResult{}
	switch {
	case toggle:
		// Anything in `ids` that exists -> detach; anything that doesn't -> attach.
		var attachIDs, detachIDs []any
		for k, v := range wantSet {
			if _, exists := currentSet[k]; exists {
				detachIDs = append(detachIDs, v)
			} else {
				attachIDs = append(attachIDs, v)
			}
		}
		if len(attachIDs) > 0 {
			if err := r.AttachRelation(parent, relation, attachIDs); err != nil {
				return nil, err
			}
		}
		if len(detachIDs) > 0 {
			if _, err := r.DetachRelation(parent, relation, detachIDs); err != nil {
				return nil, err
			}
		}
		out.Attached = attachIDs
		out.Detached = detachIDs
	default:
		// Attach anything in `wantSet` that isn't yet attached.
		var attachIDs []any
		for k, v := range wantSet {
			if _, exists := currentSet[k]; !exists {
				attachIDs = append(attachIDs, v)
			}
		}
		if len(attachIDs) > 0 {
			if err := r.AttachRelation(parent, relation, attachIDs); err != nil {
				return nil, err
			}
		}
		out.Attached = attachIDs

		if detachMissing {
			// Detach anything in `currentSet` that isn't in `wantSet`.
			var detachIDs []any
			for k, v := range currentSet {
				if _, keep := wantSet[k]; !keep {
					detachIDs = append(detachIDs, v)
				}
			}
			if len(detachIDs) > 0 {
				if _, err := r.DetachRelation(parent, relation, detachIDs); err != nil {
					return nil, err
				}
			}
			out.Detached = detachIDs
		}
	}

	return out, nil
}

// UpdateExistingPivotRelation updates pivot columns for an already-attached id. No-op (returns
// 0) if no matching pivot row exists.
func (r *Query) UpdateExistingPivotRelation(parent any, relation string, id any, attrs map[string]any) (int64, error) {
	desc, parentVal, err := r.resolvePivot(parent, relation, "UpdateExistingPivot")
	if err != nil {
		return 0, err
	}
	if len(attrs) == 0 {
		return 0, nil
	}
	q := r.freshSession().Table(desc.pivotTable).
		Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.pivotParentRef.foreignColumn)), parentVal).
		Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.pivotRelatedRef.foreignColumn)), id)
	if desc.kind == relKindMorphToMany {
		q = q.Where(fmt.Sprintf("%s.%s = ?", quoteIdent(desc.pivotTable), quoteIdent(desc.morphTypeColumn)), desc.morphValue)
	}
	res := q.Updates(attrs)
	return res.RowsAffected, res.Error
}

// setRelationFKOnChild reads parent's local key, then writes that value into child's FK column
// (and the morph_type column for MorphOne/MorphMany). Mutates child in place; child must be a
// pointer to a struct.
func (r *Query) setRelationFKOnChild(parent, child any, desc *relationDescriptor) error {
	if len(desc.references) == 0 {
		return errors.OrmRelationUnsupported.Args(desc.name, desc.parentTable, "no references")
	}
	ref := desc.references[0]
	parentVal, err := readParentColumn(r, parent, ref.primaryColumn)
	if err != nil {
		return err
	}
	childSchema, err := parseGormSchema(r.instance, child)
	if err != nil {
		return err
	}
	fkField, ok := childSchema.FieldsByDBName[ref.foreignColumn]
	if !ok {
		return errors.OrmRelationUnsupported.Args(desc.name, childSchema.Name, "no FK field "+ref.foreignColumn)
	}
	rv := reflect.ValueOf(child).Elem()
	if err := fkField.Set(r.ctx, rv, parentVal); err != nil {
		return err
	}
	if desc.kind == relKindMorphOne || desc.kind == relKindMorphMany {
		typeField, ok := childSchema.FieldsByDBName[desc.morphTypeColumn]
		if !ok {
			return errors.OrmRelationUnsupported.Args(desc.name, childSchema.Name, "no morph type field "+desc.morphTypeColumn)
		}
		if err := typeField.Set(r.ctx, rv, desc.morphValue); err != nil {
			return err
		}
	}
	return nil
}

// kindName returns a human-friendly name for a relationKind, used in error messages.
func kindName(k relationKind) string {
	switch k {
	case relKindHasOne:
		return "hasOne"
	case relKindHasMany:
		return "hasMany"
	case relKindBelongsTo:
		return "belongsTo"
	case relKindMany2Many:
		return "many2Many"
	case relKindMorphOne:
		return "morphOne"
	case relKindMorphMany:
		return "morphMany"
	case relKindMorphTo:
		return "morphTo"
	case relKindMorphToMany:
		return "morphToMany"
	case relKindHasOneThrough:
		return "hasOneThrough"
	case relKindHasManyThrough:
		return "hasManyThrough"
	}
	return fmt.Sprintf("kind=%d", k)
}
package gorm

import (
	"testing"

	"github.com/stretchr/testify/assert"

	contractsorm "github.com/goravel/framework/contracts/database/orm"
	"github.com/goravel/framework/errors"
)

// setRelationFKOnChild is the FK-and-morph-type writer that SaveRelation calls before
// persistence. We test it directly because the stub dialector can't run the INSERT step.

func TestSetRelationFKOnChild_HasMany(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	desc, err := resolveRelation(q.instance, &relUser{}, "Books")
	assert.NoError(t, err)

	parent := &relUser{ID: 7}
	child := &relBook{Title: "x"}
	err = q.setRelationFKOnChild(parent, child, desc)
	assert.NoError(t, err)
	assert.Equal(t, uint(7), child.UserID)
}

func TestSetRelationFKOnChild_MorphMany(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	desc, err := resolveRelation(q.instance, &relUser{}, "Houses")
	assert.NoError(t, err)

	parent := &relUser{ID: 9}
	child := &relHouse{Address: "x"}
	err = q.setRelationFKOnChild(parent, child, desc)
	assert.NoError(t, err)
	assert.Equal(t, uint(9), child.HouseableID)
	assert.Equal(t, "rel_users", child.HouseableType)
}

func TestSetRelationFKOnChild_MorphOne(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	desc, err := resolveRelation(q.instance, &relUser{}, "Logo")
	assert.NoError(t, err)

	parent := &relUser{ID: 11}
	child := &relLogo{URL: "x"}
	err = q.setRelationFKOnChild(parent, child, desc)
	assert.NoError(t, err)
	assert.Equal(t, uint(11), child.LogoableID)
	assert.Equal(t, "rel_users", child.LogoableType)
}

// SaveRelation guard / dispatch tests. These don't reach the INSERT step (they error / return
// early before persistence), so they're safe to run against the stub dialector.

func TestSaveRelation_NotPointerParent(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.SaveRelation(relUser{ID: 1}, "Books", &relBook{})
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestSaveRelation_NilChild(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.SaveRelation(&relUser{ID: 1}, "Books", nil)
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestSaveRelation_UnsupportedKind_BelongsTo(t *testing.T) {
	q := newRelQueryWith(t, &relBook{})
	err := q.SaveRelation(&relBook{}, "Author", &relUser{})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestSaveRelation_UnsupportedKind_HasManyThrough(t *testing.T) {
	q := newRelQueryWith(t, &relCountry{})
	err := q.SaveRelation(&relCountry{}, "Posts", &relPost{})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestSaveRelation_RelationNotFound(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.SaveRelation(&relUser{}, "DoesNotExist", &relBook{})
	assert.True(t, errors.Is(err, errors.OrmRelationNotFound))
}

func TestSaveManyRelation_NonSlice(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.SaveManyRelation(&relUser{ID: 1}, "Books", "not a slice")
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

// Sanity: *Query satisfies the helper signatures the Orm wrapper relies on, and the
// contractsorm.Query interface is still satisfied (the new methods don't break that contract).
var _ interface {
	SaveRelation(parent any, relation string, child any) error
	SaveManyRelation(parent any, relation string, children any) error
	AssociateRelation(parent any, relation string, owner any) error
	DissociateRelation(parent any, relation string) error
} = (*Query)(nil)
var _ contractsorm.Query = (*Query)(nil)

// --- Sync / Toggle / UpdateExistingPivot ----------------------------------

func TestSyncRelation_NotPointerParent(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.SyncRelation(relUser{}, "Roles", []any{1, 2})
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestSyncRelation_UnsupportedKind(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.SyncRelation(&relUser{ID: 1}, "Books", []any{1})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestSyncWithoutDetachingRelation_UnsupportedKind(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.SyncWithoutDetachingRelation(&relUser{ID: 1}, "Books", []any{1})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestToggleRelation_UnsupportedKind(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.ToggleRelation(&relUser{ID: 1}, "Books", []any{1})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestUpdateExistingPivotRelation_NotPointerParent(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.UpdateExistingPivotRelation(relUser{}, "Roles", 1, map[string]any{"x": 1})
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestUpdateExistingPivotRelation_UnsupportedKind(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.UpdateExistingPivotRelation(&relUser{ID: 1}, "Books", 1, map[string]any{"x": 1})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestUpdateExistingPivotRelation_EmptyAttrs_NoOp(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	rows, err := q.UpdateExistingPivotRelation(&relUser{ID: 1}, "Roles", 1, map[string]any{})
	assert.NoError(t, err)
	assert.Equal(t, int64(0), rows)
}

func TestBasePivotRow_Many2Many(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	desc, err := resolveRelation(q.instance, &relUser{}, "Roles")
	assert.NoError(t, err)

	row := q.basePivotRow(desc, uint(7), uint(99), nil)
	assert.Equal(t, uint(7), row[desc.pivotParentRef.foreignColumn])
	assert.Equal(t, uint(99), row[desc.pivotRelatedRef.foreignColumn])
	_, hasMorphType := row[desc.morphTypeColumn]
	assert.False(t, hasMorphType, "pure Many2Many must not include morph_type")
}

func TestBasePivotRow_MorphToMany_IncludesType(t *testing.T) {
	q := newRelQueryWith(t, &morphPost{})
	desc, err := resolveRelation(q.instance, &morphPost{}, "Tags")
	assert.NoError(t, err)

	row := q.basePivotRow(desc, uint(3), uint(11), nil)
	assert.Equal(t, uint(3), row["taggable_id"])
	assert.Equal(t, "morph_posts", row["taggable_type"]) // table-name fallback
	assert.Equal(t, uint(11), row[desc.pivotRelatedRef.foreignColumn])
}

func TestBasePivotRow_AttrsOverlay(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	desc, err := resolveRelation(q.instance, &relUser{}, "Roles")
	assert.NoError(t, err)

	row := q.basePivotRow(desc, uint(7), uint(99), map[string]any{
		"priority": "high",
		"notes":    "x",
	})
	assert.Equal(t, "high", row["priority"])
	assert.Equal(t, "x", row["notes"])
	assert.Equal(t, uint(7), row[desc.pivotParentRef.foreignColumn])
}

func TestAttachRelation_NotPointerParent(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.AttachRelation(relUser{}, "Roles", []any{1})
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestAttachRelation_UnsupportedKind_HasMany(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.AttachRelation(&relUser{ID: 1}, "Books", []any{1})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestAttachRelation_EmptyIDs_NoOp(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.AttachRelation(&relUser{ID: 1}, "Roles", nil)
	assert.NoError(t, err)
}

func TestAttachWithPivotRelation_NotPointerParent(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.AttachWithPivotRelation(relUser{}, "Roles", map[any]map[string]any{1: {"priority": "high"}})
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestAttachWithPivotRelation_EmptyMap_NoOp(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.AttachWithPivotRelation(&relUser{ID: 1}, "Roles", map[any]map[string]any{})
	assert.NoError(t, err)
}

func TestDetachRelation_UnsupportedKind_HasMany(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.DetachRelation(&relUser{ID: 1}, "Books", nil)
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestDetachRelation_NotPointerParent(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	_, err := q.DetachRelation(relUser{}, "Roles", nil)
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestMutateAssociate_BelongsTo_SetsFK(t *testing.T) {
	q := newRelQueryWith(t, &relBook{})
	desc, err := resolveRelation(q.instance, &relBook{}, "Author")
	assert.NoError(t, err)

	parent := &relBook{Title: "x", AuthorID: 0}
	owner := &relUser{ID: 42}
	err = q.mutateAssociate(parent, owner, desc, false)
	assert.NoError(t, err)
	assert.Equal(t, uint(42), parent.AuthorID)
}

func TestMutateDissociate_BelongsTo_ClearsFK(t *testing.T) {
	q := newRelQueryWith(t, &relBook{})
	desc, err := resolveRelation(q.instance, &relBook{}, "Author")
	assert.NoError(t, err)

	parent := &relBook{Title: "x", AuthorID: 99}
	err = q.mutateDissociate(parent, desc, false)
	assert.NoError(t, err)
	assert.Equal(t, uint(0), parent.AuthorID)
}

func TestAssociateRelation_NotPointerParent(t *testing.T) {
	q := newRelQueryWith(t, &relBook{})
	err := q.AssociateRelation(relBook{}, "Author", &relUser{ID: 1})
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestAssociateRelation_NilOwner(t *testing.T) {
	q := newRelQueryWith(t, &relBook{})
	err := q.AssociateRelation(&relBook{}, "Author", nil)
	assert.True(t, errors.Is(err, errors.OrmNewRelationParentNotPointer))
}

func TestAssociateRelation_UnsupportedKind_HasMany(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.AssociateRelation(&relUser{}, "Books", &relBook{})
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

func TestDissociateRelation_UnsupportedKind_HasMany(t *testing.T) {
	q := newRelQueryWith(t, &relUser{})
	err := q.DissociateRelation(&relUser{}, "Books")
	assert.True(t, errors.Is(err, errors.OrmRelationKindNotSupported))
}

// MorphTo Associate / Dissociate exercise the morph_type column path. Uses the morphImage
// fixture from relation_test.go.

func TestMutateAssociate_MorphTo_SetsFKAndType(t *testing.T) {
	q := newRelQueryWith(t, &morphImage{})
	desc, err := resolveRelation(q.instance, &morphImage{}, "Imageable")
	assert.NoError(t, err)

	parent := &morphImage{}
	owner := &relUser{ID: 3, Name: "n"}
	err = q.mutateAssociate(parent, owner, desc, true)
	assert.NoError(t, err)
	assert.Equal(t, uint(3), parent.ImageableID)
	assert.Equal(t, "rel_users", parent.ImageableType) // table-name fallback when not registered
}

func TestMutateDissociate_MorphTo_ClearsFKAndType(t *testing.T) {
	q := newRelQueryWith(t, &morphImage{})
	desc, err := resolveRelation(q.instance, &morphImage{}, "Imageable")
	assert.NoError(t, err)

	parent := &morphImage{ImageableID: 5, ImageableType: "post"}
	err = q.mutateDissociate(parent, desc, true)
	assert.NoError(t, err)
	assert.Equal(t, uint(0), parent.ImageableID)
	assert.Equal(t, "", parent.ImageableType)
}
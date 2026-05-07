package gorm

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	contractsdatabase "github.com/goravel/framework/contracts/database"
	"github.com/goravel/framework/errors"
)

// --- Pure helpers ----------------------------------------------------------

func TestDictKey(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"nil", nil, ""},
		{"string", "abc", "abc"},
		{"bytes", []byte("abc"), "abc"},
		{"int", 42, "42"},
		{"int64", int64(42), "42"},
		{"uint", uint(7), "7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dictKey(tc.input))
		})
	}
}

func TestContainsCol(t *testing.T) {
	assert.True(t, containsCol([]string{"id", "name"}, "id"))
	assert.True(t, containsCol([]string{"users.id", "name"}, "id"))
	assert.False(t, containsCol([]string{"name"}, "id"))
	assert.False(t, containsCol(nil, "id"))
}

func TestChunkSize(t *testing.T) {
	q := newRelQuery(t)
	// nil config -> default
	assert.Equal(t, defaultEagerLoadChunkSize, q.chunkSize())
}

// --- Reflect helpers ------------------------------------------------------

func TestCollectEagerParentsStructPtr(t *testing.T) {
	u := &relUser{ID: 1}
	out, err := collectEagerParents(u)
	assert.NoError(t, err)
	assert.Len(t, out, 1)
}

func TestCollectEagerParentsSlice(t *testing.T) {
	users := []relUser{{ID: 1}, {ID: 2}}
	out, err := collectEagerParents(&users)
	assert.NoError(t, err)
	assert.Len(t, out, 2)
}

func TestCollectEagerParentsSliceOfPtr(t *testing.T) {
	users := []*relUser{{ID: 1}, nil, {ID: 2}}
	out, err := collectEagerParents(&users)
	assert.NoError(t, err)
	assert.Len(t, out, 2)
}

func TestCollectEagerParentsNil(t *testing.T) {
	out, err := collectEagerParents(nil)
	assert.NoError(t, err)
	assert.Nil(t, out)

	var p *relUser
	out, err = collectEagerParents(p)
	assert.NoError(t, err)
	assert.Nil(t, out)
}

func TestCollectEagerParentsNotPointer(t *testing.T) {
	out, err := collectEagerParents(relUser{})
	assert.NoError(t, err)
	assert.Nil(t, out)
}

func TestCollectEagerParentsUnsupportedKind(t *testing.T) {
	v := 7
	out, err := collectEagerParents(&v)
	assert.NoError(t, err)
	assert.Nil(t, out)
}

func TestNewSampleModel(t *testing.T) {
	u := relUser{ID: 1}
	rv := reflect.ValueOf(u)
	got := newSampleModel(rv)
	rt := reflect.TypeOf(got)
	assert.Equal(t, reflect.Pointer, rt.Kind())
	assert.Equal(t, "relUser", rt.Elem().Name())
	// Should be a fresh zero instance (not the original).
	assert.Equal(t, uint(0), got.(*relUser).ID)
}

func TestParseGormSchema(t *testing.T) {
	db := newStubGormDB(t)
	s, err := parseGormSchema(db, &relUser{})
	assert.NoError(t, err)
	assert.Equal(t, "rel_users", s.Table)

	_, err = parseGormSchema(db, "bad-model")
	assert.Error(t, err)
}

func TestExtractKeysDeduplicatesAndSkipsZero(t *testing.T) {
	db := newStubGormDB(t)
	s, err := parseGormSchema(db, &relUser{})
	assert.NoError(t, err)
	idField := s.FieldsByDBName["id"]
	assert.NotNil(t, idField)

	q := NewQuery(context.Background(), nil, contractsdatabase.Config{}, db, nil, nil, nil, &Conditions{})

	parents := []reflect.Value{
		reflect.ValueOf(relUser{ID: 1}),
		reflect.ValueOf(relUser{ID: 2}),
		reflect.ValueOf(relUser{ID: 1}), // dup
		reflect.ValueOf(relUser{ID: 0}), // zero - skipped
	}
	keys := extractKeys(q, parents, idField)
	assert.Len(t, keys, 2)
}

// --- setRelationField ------------------------------------------------------

type withPtrRel struct {
	ID      uint
	Profile *relProfile
}

type withSlicePtrRel struct {
	ID    uint
	Books []*relBook
}

type withSliceStructRel struct {
	ID    uint
	Books []relBook
}

func TestSetRelationField_PtrAssignment(t *testing.T) {
	parent := withPtrRel{}
	rv := reflect.ValueOf(&parent).Elem()
	row := reflect.ValueOf(&relProfile{Bio: "x"})
	err := setRelationField(rv, "Profile", []reflect.Value{row})
	assert.NoError(t, err)
	assert.Equal(t, "x", parent.Profile.Bio)
}

func TestSetRelationField_PtrEmptyClearsField(t *testing.T) {
	parent := withPtrRel{Profile: &relProfile{Bio: "stale"}}
	rv := reflect.ValueOf(&parent).Elem()
	err := setRelationField(rv, "Profile", nil)
	assert.NoError(t, err)
	assert.Nil(t, parent.Profile)
}

func TestSetRelationField_SliceOfPtrs(t *testing.T) {
	parent := withSlicePtrRel{}
	rv := reflect.ValueOf(&parent).Elem()
	rows := []reflect.Value{
		reflect.ValueOf(&relBook{Title: "a"}),
		reflect.ValueOf(&relBook{Title: "b"}),
	}
	err := setRelationField(rv, "Books", rows)
	assert.NoError(t, err)
	assert.Len(t, parent.Books, 2)
}

func TestSetRelationField_SliceOfStructs(t *testing.T) {
	parent := withSliceStructRel{}
	rv := reflect.ValueOf(&parent).Elem()
	rows := []reflect.Value{
		reflect.ValueOf(&relBook{Title: "a"}),
		reflect.ValueOf(&relBook{Title: "b"}),
	}
	err := setRelationField(rv, "Books", rows)
	assert.NoError(t, err)
	assert.Len(t, parent.Books, 2)
	assert.Equal(t, "a", parent.Books[0].Title)
}

func TestSetRelationField_UnknownField(t *testing.T) {
	parent := withPtrRel{}
	rv := reflect.ValueOf(&parent).Elem()
	err := setRelationField(rv, "Missing", nil)
	assert.True(t, errors.Is(err, errors.OrmEagerLoadCannotAssign))
}

// withInterfaceRel exercises the MorphTo field shape: an `any` field that the loader fills with
// a *RelatedModel value chosen at runtime via the morph map.
type withInterfaceRel struct {
	ID        uint
	Imageable any
}

func TestSetRelationField_InterfaceAssignment(t *testing.T) {
	parent := withInterfaceRel{}
	rv := reflect.ValueOf(&parent).Elem()
	row := reflect.ValueOf(&relBook{Title: "x"})
	err := setRelationField(rv, "Imageable", []reflect.Value{row})
	assert.NoError(t, err)
	got, ok := parent.Imageable.(*relBook)
	assert.True(t, ok)
	assert.Equal(t, "x", got.Title)
}

func TestSetRelationField_InterfaceEmptyClearsField(t *testing.T) {
	parent := withInterfaceRel{Imageable: &relBook{Title: "stale"}}
	rv := reflect.ValueOf(&parent).Elem()
	err := setRelationField(rv, "Imageable", nil)
	assert.NoError(t, err)
	assert.Nil(t, parent.Imageable)
}

// --- runEagerLoads no-op paths --------------------------------------------

func TestRunEagerLoadsNoParents(t *testing.T) {
	q := newRelQuery(t)
	err := q.runEagerLoads(nil, []eagerLoadEntry{{relation: "Books"}})
	assert.NoError(t, err)
}

func TestRunEagerLoadsNoEntries(t *testing.T) {
	q := newRelQuery(t)
	parents := []reflect.Value{reflect.ValueOf(relUser{ID: 1})}
	err := q.runEagerLoads(parents, nil)
	assert.NoError(t, err)
}

func TestApplyEagerLoadsNothingQueued(t *testing.T) {
	q := newRelQuery(t)
	users := &[]relUser{}
	err := q.applyEagerLoads(users)
	assert.NoError(t, err)
}

func TestApplyEagerLoadsEmptyDest(t *testing.T) {
	q := newRelQuery(t)
	q.conditions.eagerLoad = []eagerLoadEntry{{relation: "Books"}}
	users := &[]relUser{}
	err := q.applyEagerLoads(users)
	assert.NoError(t, err)
}

func TestRecurseNestedNoop(t *testing.T) {
	q := newRelQuery(t)
	err := q.recurseNested(nil, []eagerLoadEntry{{relation: "X"}})
	assert.NoError(t, err)
	err = q.recurseNested([]reflect.Value{reflect.ValueOf(relUser{})}, nil)
	assert.NoError(t, err)
}

func TestMaybeRecurseEmpty_NotMany(t *testing.T) {
	q := newRelQuery(t)
	err := q.maybeRecurseEmpty(nil, "X", false, nil)
	assert.NoError(t, err)
}

func TestMaybeRecurseEmpty_ManyAssignsEmptySlices(t *testing.T) {
	q := newRelQuery(t)
	u1 := &withSlicePtrRel{ID: 1, Books: []*relBook{{Title: "a"}}}
	u2 := &withSlicePtrRel{ID: 2}
	parents := []reflect.Value{reflect.ValueOf(u1).Elem(), reflect.ValueOf(u2).Elem()}
	err := q.maybeRecurseEmpty(parents, "Books", true, nil)
	assert.NoError(t, err)
	assert.Empty(t, u1.Books)
	assert.Empty(t, u2.Books)
}

package page

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kovetskiy/mark/v16/confluence"
	"github.com/kovetskiy/mark/v16/confluence/confluencetest"
	"github.com/kovetskiy/mark/v16/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func orderedAPI(t *testing.T) (*confluence.API, *confluencetest.Server) {
	t.Helper()
	server := confluencetest.New(t)

	return confluence.NewAPI(server.URL, "user", "token", false), server
}

// stubAncestryTracker is an AncestryTracker holding a fixed mapping.
type stubAncestryTracker struct {
	stubFolderTracker
	pages    map[string]string
	recorded map[string]string
}

func (s *stubAncestryTracker) LookupParent(_, path string) (string, bool, error) {
	id, ok := s.pages[path]

	return id, ok, nil
}

func (s *stubAncestryTracker) RecordParent(_, path, id string) error {
	s.recorded[path] = id

	return nil
}

func newStubTracker() *stubAncestryTracker {
	recorded := map[string]string{}

	return &stubAncestryTracker{
		stubFolderTracker: stubFolderTracker{
			folders:  map[string]string{},
			recorded: recorded,
		},
		pages:    map[string]string{},
		recorded: recorded,
	}
}

func TestEnsureOrderedAncestryEmptyAncestryReturnsNil(t *testing.T) {
	parent, err := EnsureOrderedAncestry(true, nil, "DOCS", nil, nil)
	assert.NoError(t, err)
	assert.Nil(t, parent)
}

func TestEnsureOrderedAncestryRejectsUnknownType(t *testing.T) {
	_, err := EnsureOrderedAncestry(true, nil, "DOCS", []metadata.Ancestor{
		{Type: "whiteboard", Title: "Nope"},
	}, nil)
	assert.ErrorContains(t, err, "unknown type")
}

// TestEnsureOrderedAncestryInterleaves is the reason the walker exists:
// page > folder > page > folder is a shape EnsureMixedAncestry cannot express,
// because it resolves every anchor page first and nests every folder below them.
func TestEnsureOrderedAncestryInterleaves(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")
	root := server.AddPage("DOCS", "Root", "page", "")
	guides := server.AddFolder("DOCS", "Guides", root.ID, "page")
	deploy := server.AddPage("DOCS", "Deploy", "page", guides.ID)
	server.AddFolder("DOCS", "Runbooks", deploy.ID, "page")

	parent, err := EnsureOrderedAncestry(false, api, "DOCS", []metadata.Ancestor{
		{Type: metadata.AncestorPage, Title: "Root"},
		{Type: metadata.AncestorFolder, Title: "Guides"},
		{Type: metadata.AncestorPage, Title: "Deploy"},
		{Type: metadata.AncestorFolder, Title: "Runbooks"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, parent)

	assert.Equal(t, "Runbooks", parent.Title)
	assert.Equal(t, "folder", parent.Type)
	assert.Equal(t, 0, server.CountRequests("POST", "/rest/api/content"),
		"nothing should be created when the whole chain already exists")
}

// TestEnsureOrderedAncestryRecordsWhatItResolved: what the walker records is
// what makes a rename survivable on the next run, and a page and a folder of
// the same name are different objects, so the kind is part of the key.
func TestEnsureOrderedAncestryRecordsWhatItResolved(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")
	root := server.AddPage("DOCS", "Root", "page", "")
	guides := server.AddFolder("DOCS", "Guides", root.ID, "page")

	tracker := newStubTracker()

	_, err := EnsureOrderedAncestry(false, api, "DOCS", []metadata.Ancestor{
		{Type: metadata.AncestorPage, Title: "Root"},
		{Type: metadata.AncestorFolder, Title: "Guides"},
	}, tracker)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"page:Root":                  root.ID,
		"page:Root\x00folder:Guides": guides.ID,
	}, tracker.recorded)
}

// TestEnsureOrderedAncestryFollowsARenamedFolder: a folder renamed in
// Confluence no longer answers to the title the document declares. Creating a
// second one beside it splits the hierarchy, and every page below moves into
// the new empty half.
func TestEnsureOrderedAncestryFollowsARenamedFolder(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")
	root := server.AddPage("DOCS", "Root", "page", "")
	guides := server.AddFolder("DOCS", "Guides", root.ID, "page")
	server.RenameFolder(guides.ID, "Handbook")

	tracker := newStubTracker()
	tracker.folders["page:Root\x00folder:Guides"] = guides.ID

	parent, err := EnsureOrderedAncestry(false, api, "DOCS", []metadata.Ancestor{
		{Type: metadata.AncestorPage, Title: "Root"},
		{Type: metadata.AncestorFolder, Title: "Guides"},
	}, tracker)
	require.NoError(t, err)
	require.NotNil(t, parent)

	assert.Equal(t, guides.ID, parent.ID)
	assert.Equal(t, "Handbook", parent.Title)
	assert.Equal(t, 0, server.CountRequests("POST", "/api/v2/folders"),
		"the recorded folder is the one the document means; another must not be created")
}

// TestEnsureOrderedAncestryDryRunRecordsNothing: a dry run's ids name nothing,
// so recording one would have the next real run adopt it as this chain's page.
func TestEnsureOrderedAncestryDryRunRecordsNothing(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")

	tracker := newStubTracker()

	_, err := EnsureOrderedAncestry(true, api, "DOCS", []metadata.Ancestor{
		{Type: metadata.AncestorPage, Title: "Root"},
		{Type: metadata.AncestorFolder, Title: "Guides"},
	}, tracker)
	require.NoError(t, err)

	assert.Empty(t, tracker.recorded)
	assert.Equal(t, 0, server.CountRequests("POST", "/rest/api/content"))
	assert.Equal(t, 0, server.CountRequests("POST", "/api/v2/folders"))
}

func TestIsTitleConflictError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unrelated error", errors.New("network unreachable"), false},
		{"v1 phrase from Confluence Cloud", errors.New(`status: 400 Bad Request, output: "{\"message\":\"A page already exists with the same TITLE in this space\"}"`), true},
		{"alternative phrase", errors.New(`A page with this title already exists`), true},
		{"folder phrase", errors.New(`a folder exists with the same title`), true},
		{"wrapped error preserves match", fmt.Errorf("create page %q: %w", "Foo", errors.New("A page already exists")), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTitleConflictError(tc.err))
		})
	}
}

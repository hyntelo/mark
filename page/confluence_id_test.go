package page

import (
	"net/http"
	"testing"

	"github.com/kovetskiy/mark/v16/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindExistingPageByRecordedID: the whole point of confluence_id is that
// the page keeps its identity when the document is retitled. Looked up by
// title, a retitled document finds nothing and publishes a second page.
func TestFindExistingPageByRecordedID(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")
	published := server.AddPage("DOCS", "Old Title", "page", "")

	found, err := findExistingPage(api, &metadata.Meta{
		ID: published.ID, Space: "DOCS", Title: "New Title", Type: "page",
	})
	require.NoError(t, err)
	require.NotNil(t, found)

	assert.Equal(t, published.ID, found.ID)
	assert.Equal(t, "Old Title", found.Title, "the rename is staged by the caller, not here")
}

// TestFindExistingPageIgnoresAnIDInAnotherSpace: an id naming a page somewhere
// else is a stale stamp, most often a document copied between vaults. Honouring
// it overwrites an unrelated page, which is the one failure here that cannot be
// undone.
func TestFindExistingPageIgnoresAnIDInAnotherSpace(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")
	server.AddSpace("OTHER")
	elsewhere := server.AddPage("OTHER", "Unrelated", "page", "")
	wanted := server.AddPage("DOCS", "Guide", "page", "")

	found, err := findExistingPage(api, &metadata.Meta{
		ID: elsewhere.ID, Space: "DOCS", Title: "Guide", Type: "page",
	})
	require.NoError(t, err)
	require.NotNil(t, found)

	assert.Equal(t, wanted.ID, found.ID)
}

// TestFindExistingPageFallsBackWhenTheIDIsGone: a 404 is a page that really is
// not there any more, and the document publishes afresh under its title.
func TestFindExistingPageFallsBackWhenTheIDIsGone(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")
	wanted := server.AddPage("DOCS", "Guide", "page", "")

	found, err := findExistingPage(api, &metadata.Meta{
		ID: "9999999", Space: "DOCS", Title: "Guide", Type: "page",
	})
	require.NoError(t, err)
	require.NotNil(t, found)

	assert.Equal(t, wanted.ID, found.ID)
}

// TestFindExistingPageRefusesAFailedRead: a read that failed is not a page that
// is gone. Falling back on a 403 or a 5xx publishes a duplicate and the run
// that did it looks like it worked.
func TestFindExistingPageRefusesAFailedRead(t *testing.T) {
	api, server := orderedAPI(t)
	server.AddSpace("DOCS")
	published := server.AddPage("DOCS", "Guide", "page", "")

	server.SetFail(func(r *http.Request) (int, string, bool) {
		if r.Method == http.MethodGet && r.URL.Path == "/rest/api/content/"+published.ID {
			return http.StatusForbidden, `{"message":"no"}`, true
		}

		return 0, "", false
	})

	_, err := findExistingPage(api, &metadata.Meta{
		ID: published.ID, Space: "DOCS", Title: "Guide", Type: "page",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), published.ID)
}

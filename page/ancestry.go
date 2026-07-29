package page

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/kovetskiy/mark/v16/confluence"
	"github.com/kovetskiy/mark/v16/metadata"
	"github.com/rs/zerolog/log"
)

// ParentInfo represents either a page or folder parent
type ParentInfo struct {
	ID    string
	Title string
	Type  string // "page" or "folder"
}

// Process-wide cache of folders created or found during a run, so that files
// sharing a folder ancestry do not each re-resolve it.
//
// The mutex is not optional even though mark currently syncs files
// sequentially: the map is package state, so a caller using the library from
// more than one goroutine -- or any future move to parallel file processing --
// would otherwise corrupt it, and an unsynchronised map write is a hard
// runtime throw rather than a subtle wrong answer.
var (
	createdFolderCache = map[string]string{}
	createdFolderMutex sync.RWMutex
)

// ResetFolderCache empties the folder cache.
//
// The cache is package-level and keyed only by space, parent and title, with no
// notion of which Confluence it came from. That is fine within one run and
// wrong across two: Run is a public entry point, so a process can publish twice
// -- to different instances, even -- and the second would resolve folders to
// ids the first saw. It also means a folder renamed or deleted between runs is
// never looked up again.
//
// Run calls this before it starts, so a run never inherits another's answers.
func ResetFolderCache() {
	createdFolderMutex.Lock()
	defer createdFolderMutex.Unlock()
	clear(createdFolderCache)
}

func folderCacheKey(space, contextID, title string) string {
	return space + "\x00" + contextID + "\x00" + title
}

func cacheFolder(space, contextID, title, id string) {
	createdFolderMutex.Lock()
	defer createdFolderMutex.Unlock()
	createdFolderCache[folderCacheKey(space, contextID, title)] = id
}

func cachedFolderID(space, contextID, title string) (string, bool) {
	createdFolderMutex.RLock()
	defer createdFolderMutex.RUnlock()
	id, ok := createdFolderCache[folderCacheKey(space, contextID, title)]
	return id, ok
}

// resolveFolder finds the folder a title names, and may move one that an
// earlier sync left at the space root.
//
// It takes dryRun because that move is a write, and it happens while the
// ancestry is still being worked out -- long before the guard that decides
// whether this run writes anything. A run that promises to write nothing was
// reparenting a folder, and every page inside it with it.
func resolveFolder(
	api *confluence.API,
	dryRun bool,
	space, title, underID string,
	anchorPageID *string,
) (*confluence.FolderInfo, error) {
	folder, err := api.FindFolder(space, title, underID)
	if err != nil {
		return nil, err
	}
	if folder != nil {
		if underID == "" {
			if folder.ParentType == "folder" || folder.ParentType == "page" {
				return nil, nil
			}
		} else {
			if folder.ParentID != underID {
				return nil, nil
			}
		}
		return folder, nil
	}

	// Top-level wiki folder may exist at space root from an earlier sync; move it under MARK_PARENTS.
	if underID != "" && anchorPageID != nil && underID == *anchorPageID {
		folder, err = api.FindFolder(space, title, "")
		if err != nil || folder == nil {
			return folder, err
		}
		// Validate that the folder found at space root does not have any folder or page parent
		if folder.ParentType == "folder" || folder.ParentType == "page" {
			return nil, nil
		}
		if folder.ParentID != *anchorPageID {
			if dryRun {
				// Reported rather than done, and the folder is given back as it
				// actually is: saying where it would end up is the dry run's
				// job, and moving it there is not.
				log.Info().Msgf(
					"folder %q would be moved under the page named by --parents", title,
				)

				return folder, nil
			}

			if err := api.MoveContentAppend(folder.ID, *anchorPageID); err != nil {
				return nil, fmt.Errorf("move folder %q under MARK_PARENTS page: %w", title, err)
			}
			return api.GetFolderByID(folder.ID)
		}
	}

	return nil, nil
}

// EnsureFolderAncestry creates the folder hierarchy and returns the final parent for page creation.
// Top-level folders are created under anchorPageID (MARK_PARENTS page); nested folders nest under prior folders.
// FolderTracker remembers which Confluence folder a declared folder path
// resolved to.
//
// Folders are found by title, and mark creates one when the title is not found.
// A folder renamed in Confluence therefore stops matching the header that
// declares it, and mark builds a second folder beside the first and moves pages
// into it, splitting the hierarchy. Remembering the folder makes the rename
// survivable.
//
// An interface rather than the concrete store so this package keeps knowing
// nothing about where the mapping is kept. A nil tracker disables the whole
// mechanism, which is what every caller that does not opt in passes.
type FolderTracker interface {
	RecordFolder(space, folderPath, folderID string) error
	LookupFolder(space, folderPath string) (string, bool, error)
}

// ParentTracker remembers which Confluence page a declared parent chain
// resolved to.
//
// Parents are found by title too, so renaming one leaves every document that
// declares it pointing at a name nothing carries: mark creates an empty page
// under the old title and the real one's children end up beneath it.
// Remembering what the chain resolved to makes the rename survivable for any
// parent -- including the ones no document in the repository publishes, which
// is most of them.
//
// An interface for the reason FolderTracker is one, and a nil tracker disables
// it just the same.
type ParentTracker interface {
	RecordParent(space, parentPath, pageID string) error
	LookupParent(space, parentPath string) (string, bool, error)
}

// AncestryTracker is everything the resolver can remember about where a
// document was put.
type AncestryTracker interface {
	FolderTracker
	ParentTracker
}

// folderPathKey names a folder by the chain of titles leading to it, under the
// anchor it hangs from. The anchor is part of the key because the same chain of
// titles under a different anchor is a different folder.
// ParentPathKey names a parent by the chain of titles leading to it.
//
// No anchor, unlike folderPathKey: a parent chain is resolved from the space
// root, and the mapping is already per space, so the titles alone identify it.
//
// Exported because the chain is recorded here and consulted where a stale
// parent title is repaired, which is a decision made before resolution starts.
func ParentPathKey(chain []string) string {
	return strings.Join(chain, "\x00")
}

func folderPathKey(anchorPageID *string, folders []string, upto int) string {
	anchor := ""
	if anchorPageID != nil {
		anchor = *anchorPageID
	}
	return anchor + "\x00" + strings.Join(folders[:upto+1], "\x00")
}

func EnsureFolderAncestry(
	dryRun bool,
	api *confluence.API,
	space string,
	folders []string,
	anchorPageID *string,
	tracker FolderTracker,
) (*ParentInfo, error) {
	if len(folders) == 0 {
		return nil, nil
	}

	// Get space ID for folder API calls
	spaceID, err := api.GetSpaceID(space)
	if err != nil {
		return nil, fmt.Errorf("failed to get space ID for %q: %w", space, err)
	}

	var parent *ParentInfo
	rest := folders

	// Find existing folders from the beginning of the hierarchy
	for i, title := range folders {
		var folder *confluence.FolderInfo
		var err error

		underID := ""
		if parent != nil {
			underID = parent.ID
		} else if anchorPageID != nil {
			underID = *anchorPageID
		}

		if id, ok := cachedFolderID(space, underID, title); ok {
			folder, err = api.GetFolderByID(id)
		} else {
			folder, err = resolveFolder(api, dryRun, space, title, underID, anchorPageID)
		}
		if err != nil {
			return nil, fmt.Errorf("error finding folder with title %q: %w", title, err)
		}

		if folder == nil && tracker != nil {
			// No folder carries this title. It may have been renamed in
			// Confluence since the last run, in which case the folder recorded
			// for this position is still the right one and creating another
			// would split the hierarchy in two.
			key := folderPathKey(anchorPageID, folders, i)
			id, ok, lookupErr := tracker.LookupFolder(space, key)
			if lookupErr != nil {
				return nil, fmt.Errorf("unable to check whether folder %q was renamed: %w", title, lookupErr)
			}
			if ok {
				folder, err = api.GetFolderByID(id)
				if err != nil {
					// Not "gone": GetFolderByID answers a 404 with (nil, nil),
					// so an error here is a 401, a 403 or a 5xx that outlived
					// every retry -- most often a scoped token without folder
					// read. Taking that for absence created a second folder
					// with the same title and split the hierarchy this lookup
					// exists to hold together, and the run that did it looked
					// like it had worked.
					return nil, fmt.Errorf(
						"unable to check whether folder %q was renamed: %w", title, err,
					)
				}

				if folder == nil {
					// Recorded, and really not there any more. Fall through and
					// create it again.
					log.Warn().Msgf("folder %q was recorded as %s, which no longer exists", title, id)
				} else {
					log.Info().Msgf(
						"folder %q was renamed to %q; using it rather than creating another",
						title, folder.Title,
					)
				}
			}
		}

		if folder == nil {
			break
		}

		if tracker != nil {
			if err := tracker.RecordFolder(space, folderPathKey(anchorPageID, folders, i), folder.ID); err != nil {
				return nil, err
			}
		}

		cacheFolder(space, underID, title, folder.ID)
		log.Debug().Msgf("folder %q exists: %s", title, folder.ID)

		rest = folders[i:]
		parent = &ParentInfo{
			ID:    folder.ID,
			Title: folder.Title,
			Type:  "folder",
		}
	}

	if parent != nil {
		rest = rest[1:]
	}

	if len(rest) == 0 {
		return parent, nil
	}

	log.Debug().Msgf(
		"folders to be created: %s",
		strings.Join(rest, ` > `),
	)

	if !dryRun {
		// rest is the tail of folders that does not exist yet, so its offset
		// within folders is what makes a recorded key match on the next run.
		firstNew := len(folders) - len(rest)
		for offset, title := range rest {
			var folder *confluence.FolderInfo
			var err error

			if parent == nil {
				if anchorPageID == nil {
					return nil, fmt.Errorf(
						"cannot create top-level folder %q without a MARK_PARENTS anchor page",
						title,
					)
				}
				folder, err = api.CreateFolder(spaceID, title, anchorPageID, "page")
			} else {
				pid := parent.ID
				folder, err = api.CreateFolder(spaceID, title, &pid, "folder")
			}
			if err != nil {
				createErr := err

				underID := ""
				if parent != nil {
					underID = parent.ID
				} else if anchorPageID != nil {
					underID = *anchorPageID
				}

				// Another file in the same run may have created this folder
				// already. Asked by looking for the folder rather than by
				// reading the refusal: the "folder exists with the same title"
				// wording comes from Confluence and is not a contract, so a
				// reworded or localised instance turned a collision anybody can
				// hit into a hard failure. A create that failed for a real
				// reason leaves nothing to find, and the original error is what
				// gets reported.
				if id, ok := cachedFolderID(space, underID, title); ok {
					folder, err = api.GetFolderByID(id)
				} else {
					folder, err = resolveFolder(api, dryRun, space, title, underID, anchorPageID)
				}
				if err != nil || folder == nil {
					return nil, fmt.Errorf(
						"error creating folder with title %q: %w",
						title,
						createErr,
					)
				}

				log.Info().Msgf(
					"folder %q already exists as %s; using it rather than creating another",
					title, folder.ID,
				)
			}

			underID := ""
			if parent != nil {
				underID = parent.ID
			} else if anchorPageID != nil {
				underID = *anchorPageID
			}
			cacheFolder(space, underID, title, folder.ID)

			if tracker != nil {
				key := folderPathKey(anchorPageID, folders, firstNew+offset)
				if err := tracker.RecordFolder(space, key, folder.ID); err != nil {
					return nil, err
				}
			}

			parent = &ParentInfo{
				ID:    folder.ID,
				Title: folder.Title,
				Type:  "folder",
			}
		}
	} else {
		log.Info().Msgf(
			"skipping folder creation due to dry-run mode, need to create %d folders: %v",
			len(rest),
			rest,
		)
		// For dry-run, simulate the final parent
		if len(rest) > 0 {
			finalTitle := rest[len(rest)-1]
			parent = &ParentInfo{
				ID:    "dry-run-folder-id",
				Title: finalTitle,
				Type:  "folder",
			}
		}
	}

	return parent, nil
}

// EnsureMixedAncestry creates folders under the MARK_PARENTS anchor page, then returns a folder-parent
// marker so leaf pages are created inside the deepest folder.
func EnsureMixedAncestry(
	dryRun bool,
	api *confluence.API,
	tracker AncestryTracker,
	space string,
	folders []string,
	pages []string,
) (*confluence.PageInfo, error) {
	var anchorPageID *string

	if len(pages) > 0 {
		anchor, err := EnsureAncestry(dryRun, api, space, pages, tracker)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve MARK_PARENTS page ancestry: %w", err)
		}
		if anchor == nil {
			return nil, fmt.Errorf("MARK_PARENTS page chain %q could not be resolved", strings.Join(pages, " > "))
		}
		anchorPageID = &anchor.ID
	}

	if len(folders) == 0 {
		if len(pages) == 0 {
			return nil, nil
		}
		return EnsureAncestry(dryRun, api, space, pages, tracker)
	}

	folderParent, err := EnsureFolderAncestry(dryRun, api, space, folders, anchorPageID, tracker)
	if err != nil {
		return nil, fmt.Errorf("failed to create folder hierarchy: %w", err)
	}

	if folderParent == nil {
		if anchorPageID != nil {
			return &confluence.PageInfo{ID: *anchorPageID, Title: pages[len(pages)-1]}, nil
		}
		return nil, nil
	}

	return &confluence.PageInfo{
		ID:    folderParent.ID,
		Type:  "folder-parent",
		Title: folderParent.Title,
	}, nil
}

func EnsureAncestry(
	dryRun bool,
	api *confluence.API,
	space string,
	ancestry []string,
	tracker ParentTracker,
) (*confluence.PageInfo, error) {
	var parent *confluence.PageInfo

	rest := ancestry

	for i, title := range ancestry {
		page, err := api.FindPage(space, title, "page")
		if err != nil {
			return nil, fmt.Errorf("error during finding parent page with title %q: %w", title, err)
		}

		if page == nil {
			break
		}

		log.Debug().Msgf("parent page %q exists: %s", title, page.Links.Full)

		if tracker != nil {
			if err := tracker.RecordParent(space, ParentPathKey(ancestry[:i+1]), page.ID); err != nil {
				return nil, err
			}
		}

		rest = ancestry[i:]
		parent = page
	}

	if parent != nil {
		rest = rest[1:]
	} else {
		page, err := api.FindRootPage(space)
		if err != nil {
			return nil, fmt.Errorf("can't find root page for space %q: %w", space, err)
		}

		parent = page
	}
	if len(rest) == 0 {
		return parent, nil
	}

	log.Debug().
		Msgf(
			"empty pages under %q to be created: %s",
			parent.Title,
			strings.Join(rest, ` > `),
		)

	if !dryRun {
		// rest is the tail of ancestry that does not exist yet, so its offset
		// within ancestry is what makes a recorded key match on the next run.
		firstNew := len(ancestry) - len(rest)
		for offset, title := range rest {
			page, err := api.CreatePage(space, "page", parent, title, ``)
			if err != nil {
				return nil, fmt.Errorf("error during creating parent page with title %q: %w", title, err)
			}

			if tracker != nil {
				key := ParentPathKey(ancestry[:firstNew+offset+1])
				if err := tracker.RecordParent(space, key, page.ID); err != nil {
					return nil, err
				}
			}

			parent = page
		}
	} else {
		log.Info().
			Msgf(
				"skipping page creation due to enabled dry-run mode, "+
					"need to create %d pages: %v",
				len(rest),
				rest,
			)
	}

	return parent, nil
}

// ErrAncestryMismatch reports that a page exists but does not sit where its
// headers say it should.
//
// Distinguished from every other way ancestry resolution can fail because it is
// the one that is recoverable: the document has declared a different parent, and
// moving the page there is what it asked for. Everything else -- a page with no
// parents that is not the homepage, a chain that cannot be resolved -- is a
// genuine impasse.
var ErrAncestryMismatch = errors.New("page is not under the declared parents")

// The ids a dry run gives an ancestor it only pretended to create.
const (
	dryRunPageID   = "dry-run-page-id"
	dryRunFolderID = "dry-run-folder-id"
)

// isDryRunID reports whether an ancestor was only simulated. Everything below
// a simulated ancestor must be simulated too: its ID does not exist, and
// feeding it to a parent-scoped search yields a CQL parse error rather than an
// empty result.
func isDryRunID(id string) bool {
	return id == dryRunPageID || id == dryRunFolderID
}

// OrderedParent represents the resolved final parent for the target page.
// When the parent is a folder, the caller must use CreatePageWithFolderParent
// instead of the page-parented CreatePage. Type is "page" or "folder".
type OrderedParent struct {
	ID    string
	Title string
	Type  string
}

// EnsureOrderedAncestry walks the metadata.Ancestry list top-down. For each
// entry it locates the corresponding page or folder under the previously
// resolved parent (or under the space root for the first entry); missing
// entries are created in place. Returns the resolved final parent that the
// caller will use to create or update the target page.
//
// Unlike EnsureMixedAncestry, which hard-codes "anchor pages first, then all
// folders", this supports hierarchies that interleave the two kinds, e.g.
// page > folder > page > folder > target.
func EnsureOrderedAncestry(
	dryRun bool,
	api *confluence.API,
	space string,
	ancestry []metadata.Ancestor,
	tracker AncestryTracker,
) (*OrderedParent, error) {
	if len(ancestry) == 0 {
		return nil, nil
	}

	// Reject unknown entry types before touching the network so a malformed
	// header fails immediately instead of half-way through creating a tree.
	for i, entry := range ancestry {
		if entry.Type != metadata.AncestorPage && entry.Type != metadata.AncestorFolder {
			return nil, fmt.Errorf("ancestor[%d] %q: unknown type %q", i, entry.Title, entry.Type)
		}
	}

	spaceID, err := api.GetSpaceID(space)
	if err != nil {
		return nil, fmt.Errorf("failed to get space ID for %q: %w", space, err)
	}

	var parent *OrderedParent

	for i, entry := range ancestry {
		switch entry.Type {
		case metadata.AncestorPage:
			page, err := findOrCreatePageEntry(dryRun, api, space, parent, entry.Title, tracker, orderedPathKey(ancestry, i))
			if err != nil {
				return nil, fmt.Errorf("ancestor[%d] page %q: %w", i, entry.Title, err)
			}
			parent = page

		case metadata.AncestorFolder:
			folder, err := findOrCreateFolderEntry(dryRun, api, space, spaceID, parent, entry.Title, tracker, orderedPathKey(ancestry, i))
			if err != nil {
				return nil, fmt.Errorf("ancestor[%d] folder %q: %w", i, entry.Title, err)
			}
			parent = folder

		default:
			return nil, fmt.Errorf("ancestor[%d] %q: unknown type %q", i, entry.Title, entry.Type)
		}

		// Nothing is recorded for an ancestor a dry run only pretended to
		// create: the id names nothing, and the next real run would adopt it as
		// the page this chain resolves to.
		if tracker != nil && parent != nil && !isDryRunID(parent.ID) {
			if err := recordOrdered(tracker, space, orderedPathKey(ancestry, i), entry.Type, parent.ID); err != nil {
				return nil, err
			}
		}
	}

	return parent, nil
}

// orderedPathKey names an ancestor by the chain of entries leading to it.
//
// The kind is part of each segment because the same titles in the same order
// may name a page in one document and a folder in another, and those are
// different objects in the same space.
func orderedPathKey(ancestry []metadata.Ancestor, upto int) string {
	segments := make([]string, 0, upto+1)
	for _, entry := range ancestry[:upto+1] {
		segments = append(segments, entry.Type+":"+entry.Title)
	}

	return strings.Join(segments, "\x00")
}

func recordOrdered(tracker AncestryTracker, space, key, kind, id string) error {
	if kind == metadata.AncestorFolder {
		return tracker.RecordFolder(space, key, id)
	}

	return tracker.RecordParent(space, key, id)
}

func findOrCreatePageEntry(
	dryRun bool,
	api *confluence.API,
	space string,
	parent *OrderedParent,
	title string,
	tracker AncestryTracker,
	key string,
) (*OrderedParent, error) {
	if parent != nil && isDryRunID(parent.ID) {
		log.Info().Msgf("dry-run: would create page %q under %s %q", title, parent.Type, parent.Title)
		return &OrderedParent{ID: dryRunPageID, Title: title, Type: "page"}, nil
	}

	if parent == nil {
		// Top-level: search space-wide. Legacy "Parent: <homepage>" patterns
		// keep working because the homepage is reachable by title.
		page, err := api.FindPage(space, title, "page")
		if err != nil {
			return nil, err
		}
		if page != nil {
			log.Debug().Msgf("ancestor page %q resolved at space root: %s", title, page.ID)
			return &OrderedParent{ID: page.ID, Title: page.Title, Type: "page"}, nil
		}
		renamed, err := recordedPage(api, tracker, space, key, title)
		if err != nil {
			return nil, err
		}
		if renamed != nil {
			return renamed, nil
		}
		if dryRun {
			log.Info().Msgf("dry-run: would create top-level page %q", title)
			return &OrderedParent{ID: dryRunPageID, Title: title, Type: "page"}, nil
		}
		root, err := api.FindRootPage(space)
		if err != nil {
			return nil, fmt.Errorf("can't find root page for space %q: %w", space, err)
		}
		created, err := api.CreatePage(space, "page", root, title, "")
		if err != nil {
			return nil, fmt.Errorf("create page %q under space root: %w", title, err)
		}
		return &OrderedParent{ID: created.ID, Title: created.Title, Type: "page"}, nil
	}

	// 1. Parent-scoped CQL search.
	page, err := api.FindPageUnderParent(space, title, parent.ID)
	if err != nil {
		return nil, err
	}
	if page != nil {
		log.Debug().Msgf("ancestor page %q resolved under %s %q: %s", title, parent.Type, parent.Title, page.ID)
		return &OrderedParent{ID: page.ID, Title: page.Title, Type: "page"}, nil
	}

	// 2. Defensive fallback: space-wide find + v2 parent validation. CQL
	// `parent=` results lag the index, especially right after a parallel
	// create. If the page already exists under our parent, accept it.
	resolved, err := resolvePageBySpaceWideAndValidate(api, space, title, parent.ID)
	if err != nil {
		return nil, err
	}
	if resolved != nil {
		return resolved, nil
	}

	renamed, err := recordedPage(api, tracker, space, key, title)
	if err != nil {
		return nil, err
	}
	if renamed != nil {
		return renamed, nil
	}

	if dryRun {
		log.Info().Msgf("dry-run: would create page %q under %s %q", title, parent.Type, parent.Title)
		return &OrderedParent{ID: dryRunPageID, Title: title, Type: "page"}, nil
	}

	// 3. Create.
	var (
		created *confluence.PageInfo
		cerr    error
	)
	switch parent.Type {
	case "folder":
		created, cerr = api.CreatePageWithFolderParent(space, "page", parent.ID, title, "")
	case "page":
		created, cerr = api.CreatePage(space, "page", &confluence.PageInfo{ID: parent.ID, Title: parent.Title, Type: "page"}, title, "")
	default:
		return nil, fmt.Errorf("unknown parent type %q", parent.Type)
	}
	if cerr == nil {
		return &OrderedParent{ID: created.ID, Title: created.Title, Type: "page"}, nil
	}

	// 4. Create failed; if Confluence rejected because the title already
	// exists, retry the space-wide validator. Catches the race where a
	// concurrent push (e.g. another mark invocation pushed by a wrapper in
	// parallel) created the same page between our search and our create.
	if isTitleConflictError(cerr) {
		log.Warn().Err(cerr).Msgf("create %q failed with title conflict; retrying via space-wide resolver", title)
		retry, rerr := resolvePageBySpaceWideAndValidate(api, space, title, parent.ID)
		if rerr != nil {
			return nil, fmt.Errorf("create page %q under %s %q failed and recovery failed: create=%v recovery=%w", title, parent.Type, parent.Title, cerr, rerr)
		}
		if retry != nil {
			return retry, nil
		}
	}

	return nil, fmt.Errorf("create page %q under %s %q: %w", title, parent.Type, parent.Title, cerr)
}

// recordedPage returns the page a chain resolved to on an earlier run, when no
// page carries the declared title any more.
//
// That is what a rename in Confluence looks like from here, and taking it for
// absence creates an empty page under the old title and moves the real one's
// children beneath it.
func recordedPage(
	api *confluence.API,
	tracker AncestryTracker,
	space, key, title string,
) (*OrderedParent, error) {
	if tracker == nil {
		return nil, nil
	}

	id, ok, err := tracker.LookupParent(space, key)
	if err != nil {
		return nil, fmt.Errorf("unable to check whether page %q was renamed: %w", title, err)
	}
	if !ok {
		return nil, nil
	}

	page, err := api.GetPageByID(id)
	if err != nil {
		// Only a recorded page that is genuinely gone may be passed over.
		// Anything else -- a 401, a 403, a 5xx that outlived every retry --
		// read as absence turns a network blip into a duplicate ancestor.
		if !errors.Is(err, confluence.ErrNotFound) {
			return nil, fmt.Errorf("unable to load page %s recorded for %q: %w", id, title, err)
		}

		log.Warn().Msgf("page %q was recorded as %s, which no longer exists", title, id)

		return nil, nil
	}
	if page == nil {
		return nil, nil
	}

	log.Info().Msgf(
		"page %q was renamed to %q; using it rather than creating another", title, page.Title,
	)

	return &OrderedParent{ID: page.ID, Title: page.Title, Type: "page"}, nil
}

// resolvePageBySpaceWideAndValidate looks up `title` space-wide and verifies
// via the v2 API that the page sits directly under `expectedParentID`.
// Returns (resolved, nil) on match, (nil, nil) if no page is found,
// (nil, err) if the existing page sits under a different parent.
func resolvePageBySpaceWideAndValidate(
	api *confluence.API,
	space, title, expectedParentID string,
) (*OrderedParent, error) {
	spaceWide, err := api.FindPage(space, title, "page")
	if err != nil {
		return nil, err
	}
	if spaceWide == nil {
		return nil, nil
	}

	parentID, _, perr := api.GetPageParentInfo(spaceWide.ID)
	if perr != nil {
		log.Debug().Err(perr).Msgf("could not verify parent of %q via v2 API; treating as not-yet-resolved", title)
		return nil, nil
	}
	if parentID == expectedParentID {
		log.Warn().Msgf(
			"page %q resolved via space-wide search and validated as direct child of expected parent (id=%s); CQL parent-scoped search may have returned stale results",
			title, expectedParentID,
		)
		return &OrderedParent{ID: spaceWide.ID, Title: spaceWide.Title, Type: "page"}, nil
	}

	return nil, fmt.Errorf(
		"page %q exists in space %q under a different parent (parentId=%q; expected %q). "+
			"Either rename the new page or move the existing page under the expected parent in Confluence.",
		title, space, parentID, expectedParentID,
	)
}

func isTitleConflictError(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "A page already exists") ||
		strings.Contains(msg, "page with this title already exists") ||
		strings.Contains(msg, "folder exists with the same title")
}

func findOrCreateFolderEntry(
	dryRun bool,
	api *confluence.API,
	space, spaceID string,
	parent *OrderedParent,
	title string,
	tracker AncestryTracker,
	key string,
) (*OrderedParent, error) {
	if parent != nil && isDryRunID(parent.ID) {
		log.Info().Msgf("dry-run: would create folder %q under %s %q", title, parent.Type, parent.Title)

		return &OrderedParent{ID: dryRunFolderID, Title: title, Type: "folder"}, nil
	}

	underID := ""
	// A page ancestor doubles as the MARK_PARENTS anchor for the folders
	// below it, which lets resolveFolder relocate a folder that an earlier
	// sync left at the space root.
	var anchorPageID *string
	if parent != nil {
		underID = parent.ID
		if parent.Type == "page" {
			anchorPageID = &parent.ID
		}
	} else {
		// A Folder header with no page above it still needs an anchor:
		// Confluence Cloud parents a space's "top-level" folders to the space
		// homepage, not to the space itself. Anchoring there keeps the
		// direct-child validation meaningful instead of matching a folder of
		// the same title anywhere in the space.
		home, err := api.FindHomePage(space)
		if err != nil {
			return nil, fmt.Errorf("can't obtain home page from space %q: %w", space, err)
		}

		underID = home.ID
		anchorPageID = &home.ID
	}

	folder, err := resolveFolderEntry(api, dryRun, space, underID, title, anchorPageID)
	if err != nil {
		return nil, err
	}
	if folder == nil && parent == nil {
		// A hierarchy built by an earlier version, which created a top-level
		// folder at the space root rather than under the homepage.
		folder, err = resolveFolderEntry(api, dryRun, space, "", title, nil)
		if err != nil {
			return nil, err
		}
	}
	if folder != nil {
		log.Debug().Msgf("ancestor folder %q resolved under %q: %s", title, underID, folder.ID)
		return folder, nil
	}

	renamed, err := recordedFolder(api, tracker, space, key, title)
	if err != nil {
		return nil, err
	}
	if renamed != nil {
		cacheFolder(space, underID, title, renamed.ID)
		return renamed, nil
	}

	if dryRun {
		log.Info().Msgf("dry-run: would create folder %q under %q", title, underID)
		return &OrderedParent{ID: dryRunFolderID, Title: title, Type: "folder"}, nil
	}

	// underID is the homepage when this is a leading Folder header, so a new
	// top-level folder lands beside the ones already there.
	parentType := "page"
	if parent != nil {
		parentType = parent.Type
	}

	created, cerr := api.CreateFolder(spaceID, title, &underID, parentType)
	if cerr == nil {
		cacheFolder(space, underID, title, created.ID)
		return &OrderedParent{ID: created.ID, Title: created.Title, Type: "folder"}, nil
	}

	// Another file in the same run, or a parallel mark invocation, may have
	// created this folder between our search and our create.
	if isTitleConflictError(cerr) {
		log.Warn().Err(cerr).Msgf("create folder %q failed with title conflict; re-resolving", title)
		retry, rerr := resolveFolderEntry(api, dryRun, space, underID, title, anchorPageID)
		if rerr != nil {
			return nil, fmt.Errorf("create folder %q under %q failed and recovery failed: create=%v recovery=%w", title, underID, cerr, rerr)
		}
		if retry != nil {
			return retry, nil
		}

		return nil, fmt.Errorf(
			"folder %q exists in space %q but not under the expected parent %q. "+
				"Either rename the new folder or move the existing folder under the expected parent in Confluence.",
			title, space, underID,
		)
	}

	return nil, fmt.Errorf("create folder %q under %q: %w", title, underID, cerr)
}

// resolveFolderEntry resolves a single folder ancestor, preferring the
// process-wide cache populated by earlier files in the same run.
func resolveFolderEntry(
	api *confluence.API,
	dryRun bool,
	space, underID, title string,
	anchorPageID *string,
) (*OrderedParent, error) {
	var (
		folder *confluence.FolderInfo
		err    error
	)

	if id, ok := cachedFolderID(space, underID, title); ok {
		folder, err = api.GetFolderByID(id)
	} else {
		folder, err = resolveFolder(api, dryRun, space, title, underID, anchorPageID)
	}
	if err != nil {
		return nil, fmt.Errorf("error finding folder with title %q: %w", title, err)
	}
	if folder == nil {
		return nil, nil
	}

	cacheFolder(space, underID, title, folder.ID)

	return &OrderedParent{ID: folder.ID, Title: folder.Title, Type: "folder"}, nil
}

// recordedFolder is recordedPage for a folder.
//
// GetFolderByID answers a folder that is gone with (nil, nil), so an error here
// is a failure to read rather than an absence -- most often a scoped token
// without folder read -- and taking it for absence splits the hierarchy this
// lookup exists to hold together.
func recordedFolder(
	api *confluence.API,
	tracker AncestryTracker,
	space, key, title string,
) (*OrderedParent, error) {
	if tracker == nil {
		return nil, nil
	}

	id, ok, err := tracker.LookupFolder(space, key)
	if err != nil {
		return nil, fmt.Errorf("unable to check whether folder %q was renamed: %w", title, err)
	}
	if !ok {
		return nil, nil
	}

	folder, err := api.GetFolderByID(id)
	if err != nil {
		return nil, fmt.Errorf("unable to check whether folder %q was renamed: %w", title, err)
	}
	if folder == nil {
		log.Warn().Msgf("folder %q was recorded as %s, which no longer exists", title, id)

		return nil, nil
	}

	log.Info().Msgf(
		"folder %q was renamed to %q; using it rather than creating another", title, folder.Title,
	)

	return &OrderedParent{ID: folder.ID, Title: folder.Title, Type: "folder"}, nil
}

func ValidateAncestry(
	api *confluence.API,
	space string,
	ancestry []string,
) (*confluence.PageInfo, error) {
	page, err := api.FindPage(space, ancestry[len(ancestry)-1], "page")
	if err != nil {
		return nil, err
	}

	if page == nil {
		return nil, nil
	}

	isHomepage := false
	if len(page.Ancestors) < 1 {
		// Compared against, not used, so a space that will not give up its home
		// page must not fail every document in it. Without one to compare
		// against, a parentless page is treated as the misplacement below --
		// which is recoverable, where refusing outright was not.
		homepage, err := api.FindHomePage(space)
		if err != nil {
			log.Debug().Err(err).Msgf(
				"no home page for space %q; cannot tell whether %q is it",
				space, page.Title,
			)

			homepage = nil
		}

		if homepage != nil && page.ID == homepage.ID {
			log.Debug().Msgf("page is homepage for space %q", space)
			isHomepage = true
		} else {
			// The page sits at the root of its space and is not the homepage,
			// while a document that declares no parents is placed under the
			// space root page. So the two disagree about where it belongs --
			// which is a misplacement like any other, and is reported as one so
			// the caller can move it rather than refuse.
			//
			// It used to be a flat refusal, which left the page unpublishable
			// by mark at all: nothing mark could be told would move it, because
			// declaring the parent it ought to have is precisely what produces
			// this state.
			return page, fmt.Errorf(
				"%w: %q sits at the root of the space", ErrAncestryMismatch, page.Title,
			)
		}
	}

	if !isHomepage && len(page.Ancestors) < len(ancestry) {
		actual := []string{}
		for _, ancestor := range page.Ancestors {
			actual = append(actual, ancestor.Title)
		}

		valid := false

		if len(actual) == len(ancestry)-1 {
			broken := false
			for i := 0; i < len(actual); i++ {
				if actual[i] != ancestry[i] {
					broken = true
					break
				}
			}

			if !broken {
				if ancestry[len(ancestry)-1] == page.Title {
					valid = true
				}
			}
		}

		if !valid {
			return page, fmt.Errorf(
				"%w: title=%q, actual=[%s], expected=[%s]",
				ErrAncestryMismatch,
				page.Title, strings.Join(actual, " > "), strings.Join(ancestry, " > "),
			)
		}
	}

	for _, parent := range ancestry[:len(ancestry)-1] {
		found := false

		// skipping root article title
		for _, ancestor := range page.Ancestors {
			if ancestor.Title == parent {
				found = true
				break
			}
		}

		if !found {
			list := []string{}

			for _, ancestor := range page.Ancestors {
				list = append(list, ancestor.Title)
			}

			return page, fmt.Errorf(
				"%w: expected parent %q, actual=[%s]",
				ErrAncestryMismatch,
				parent, strings.Join(list, "; "),
			)
		}
	}

	return page, nil
}

package page

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kovetskiy/mark/v16/confluence"
	"github.com/kovetskiy/mark/v16/metadata"
	"github.com/rs/zerolog/log"
)

// findExistingPage locates the page a document is already published to. A
// recorded `confluence_id` wins over the title lookup: it is the only way to
// recognise a page whose title changed in the markdown, which would otherwise
// be published as a second page while the old one lingers under the old title.
//
// An ID that is gone, or that points into another space, falls back to the
// title lookup - publishing a duplicate is recoverable, overwriting an
// unrelated page is not.
//
// Only a 404 counts as gone. Every other failure to read -- a 401, a 403, a 5xx
// that outlived its retries -- ends the run rather than falling back, because
// the fallback publishes a second page and the run that did it looks like it
// worked.
func findExistingPage(api *confluence.API, meta *metadata.Meta) (*confluence.PageInfo, error) {
	if meta.ID != "" {
		page, err := api.GetPageByID(meta.ID)
		switch {
		case errors.Is(err, confluence.ErrNotFound):
			log.Warn().Msgf(
				"recorded confluence_id %s for page %q no longer exists; falling back to title lookup",
				meta.ID, meta.Title,
			)
		case err != nil:
			return nil, fmt.Errorf(
				"unable to read page %s recorded for %q: %w", meta.ID, meta.Title, err,
			)
		case page.Space.Key != "" && page.Space.Key != meta.Space:
			log.Warn().Msgf(
				"recorded confluence_id %s for page %q lives in space %q, not %q; falling back to title lookup",
				meta.ID, meta.Title, page.Space.Key, meta.Space,
			)
		default:
			if page.Title != meta.Title {
				log.Info().Msgf(
					"page %s will be renamed: %q -> %q",
					page.ID, page.Title, meta.Title,
				)
			}
			return page, nil
		}
	}

	return api.FindPage(meta.Space, meta.Title, meta.Type)
}

func ResolvePage(
	dryRun bool,
	api *confluence.API,
	meta *metadata.Meta,
	tracker AncestryTracker,
) (*confluence.PageInfo, *confluence.PageInfo, error) {
	if meta == nil {
		return nil, nil, fmt.Errorf("metadata is empty")
	}
	if len(meta.Folders) > 0 && !api.IsCloud() {
		return nil, nil, fmt.Errorf("folder support is currently only available on Confluence Cloud")
	}
	page, err := findExistingPage(api, meta)
	if err != nil {
		return nil, nil, fmt.Errorf("error while finding page %q: %w", meta.Title, err)
	}

	// A page matched by recorded ID is authoritative: it is the page this
	// document owns, wherever it currently sits, so it gets relocated rather
	// than duplicated.
	if page != nil && page.ID != meta.ID && len(meta.Folders) > 0 && len(meta.Parents) > 0 && !pageUnderParents(page, meta.Parents) {
		log.Warn().Msgf(
			"page %q exists outside MARK_PARENTS %q; will create or relocate under folder hierarchy",
			meta.Title,
			strings.Join(meta.Parents, " > "),
		)
		page = nil
	}

	if meta.Type == "blogpost" {
		log.Info().
			Msgf(
				"blog post will be stored as: %s",
				meta.Title,
			)

		return nil, page, nil
	}

	// The home page is only ever compared against here -- never used as a
	// parent -- so a space that will not give one up is not a reason to refuse
	// the document. Failing on it aborted every file in the space, including
	// documents whose Parent headers never needed a home page at all.
	homepage, err := api.FindHomePage(meta.Space)
	if err != nil {
		log.Debug().Err(err).Msgf(
			"no home page for space %q; publishing without comparing against one",
			meta.Space,
		)

		homepage = nil
	}

	skipHomeAncestry := false
	if homepage != nil && len(meta.Parents) > 0 {
		if homepage.Title == meta.Parents[0] {
			skipHomeAncestry = true
		}
	}

	// Handle mixed folder and page hierarchy
	var parent *confluence.PageInfo

	if len(meta.Folders) > 0 {
		// Mixed hierarchies are resolved from the ordered meta.Ancestry list,
		// which preserves the order the Parent/Folder headers appear in the
		// file, so pages and folders may interleave freely.
		ancestryPath := make([]string, 0, len(meta.Ancestry)+1)
		for _, ancestor := range meta.Ancestry {
			ancestryPath = append(ancestryPath, fmt.Sprintf("%s:%s", ancestor.Type, ancestor.Title))
		}

		log.Debug().
			Msgf(
				"resolving mixed hierarchy path: %s > %s",
				strings.Join(ancestryPath, ` > `),
				meta.Title,
			)

		resolved, err := EnsureOrderedAncestry(
			dryRun,
			api,
			meta.Space,
			meta.Ancestry,
			tracker,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("can't create ordered folder/page ancestry tree [%s]: %w",
				strings.Join(ancestryPath, ` > `),
				err,
			)
		}

		// Translate OrderedParent into the *confluence.PageInfo shape callers
		// expect. When the resolved parent is a folder, encode the
		// "folder-parent" sentinel so ProcessFile branches into
		// CreatePageWithFolderParent on creation.
		if resolved != nil {
			parent = &confluence.PageInfo{
				ID:    resolved.ID,
				Title: resolved.Title,
				Type:  resolved.Type,
			}
			if resolved.Type == "folder" {
				parent.Type = "folder-parent"
			}
		}
	} else {
		// Traditional page-only ancestry
		misplaced := false
		ancestry := meta.Parents
		if page != nil && !skipHomeAncestry {
			ancestry = append(ancestry, page.Title)
		}

		if len(ancestry) > 0 {
			existing, err := ValidateAncestry(
				api,
				meta.Space,
				ancestry,
			)
			if err != nil {
				if !errors.Is(err, ErrAncestryMismatch) {
					return nil, nil, err
				}

				// The document has declared a different parent than the one the
				// page currently sits under. Refusing the publish leaves the two
				// disagreeing and does nothing about it, so the page is moved
				// where the headers ask once the new parent is resolved.
				log.Info().Msgf(
					"page %q is not where its headers say; it will be moved: %s",
					meta.Title, err,
				)
				misplaced = true
			}

			if existing == nil {
				log.Warn().
					Msgf(
						"page %q is not found ",
						// Index ancestry, not meta.Parents: the home title is
						// appended to ancestry above, so len(ancestry)-1 is out of
						// range for meta.Parents whenever that append happened.
						ancestry[len(ancestry)-1],
					)
			}

			path := meta.Parents
			path = append(path, meta.Title)

			log.Debug().
				Msgf(
					"resolving page path: ??? > %s",
					strings.Join(path, ` > `),
				)
		}

		parent, err = EnsureAncestry(
			dryRun,
			api,
			meta.Space,
			meta.Parents,
			tracker,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("can't create ancestry tree %q: %w", strings.Join(meta.Parents, ` > `), err)
		}

		// Only a page that failed validation is moved. A page nested deeper
		// than its headers declare passes validation today -- every declared
		// parent is somewhere in its ancestry -- and moving those would tear up
		// hierarchies nobody asked to change.
		if misplaced && !dryRun && page != nil && parent != nil {
			if err := EnsurePageUnderParent(api, page, parent.ID); err != nil {
				return nil, nil, err
			}
		}
	}

	// Build the display path showing the complete hierarchy
	var displayPath []string

	if len(meta.Folders) > 0 {
		// Pages and folders in the order they were declared in the file
		for _, ancestor := range meta.Ancestry {
			displayPath = append(displayPath, ancestor.Title)
		}
	} else {
		// Traditional page hierarchy
		if parent != nil {
			for _, ancestor := range parent.Ancestors {
				displayPath = append(displayPath, ancestor.Title)
			}
			displayPath = append(displayPath, parent.Title)
		}
	}

	if len(displayPath) > 0 {
		log.Info().Msgf(
			"page will be stored under path: %s > %s",
			strings.Join(displayPath, ` > `),
			meta.Title,
		)
	} else {
		log.Info().Msgf(
			"page will be stored at space root: %s",
			meta.Title,
		)
	}

	return parent, page, nil
}

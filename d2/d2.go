package d2

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	stdhtml "html"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/kovetskiy/mark/v16/attachment"
	"github.com/kovetskiy/mark/v16/chrome"
	"github.com/rs/zerolog/log"

	"github.com/d2lang/d2/d2graph"
	"github.com/d2lang/d2/d2layouts/d2dagrelayout"
	"github.com/d2lang/d2/d2lib"
	"github.com/d2lang/d2/d2renderers/d2svg"
	"github.com/d2lang/d2/d2themes/d2themescatalog"
	"github.com/d2lang/d2/lib/imgbundler"
	d2log "github.com/d2lang/d2/lib/log"
	"github.com/d2lang/d2/lib/textmeasure"
	"github.com/d2lang/util-go/go2"
)

// ErrUnsafeDiagram is returned for a diagram that would do something when it is
// rendered or opened, rather than depict something.
var ErrUnsafeDiagram = errors.New("diagram is not safe to publish")

var renderTimeout = 120 * time.Second

// diagramPad is the margin d2 draws around a diagram. It is named because the
// size of the picture depends on it: the drawing is the bounding box of what
// was laid out, with this much added on every side.
const diagramPad = 5

// renderSVG compiles a diagram and draws it, which is where both outputs start:
// the PNG is this rasterised, and the SVG is this.
//
// The size comes back with it, because d2 knows it: the drawing is the bounding
// box of what it laid out plus the pad on every side. Reading it back out of
// the rendered XML would be parsing an answer that was already given -- and
// mermaid has to do exactly that, since its diagrams are drawn by a browser
// rather than by a library that will say.
func renderSVG(ctx context.Context, d2Diagram []byte) (out []byte, width, height int, err error) {
	ruler, err := textmeasure.NewRuler()
	if err != nil {
		return nil, 0, 0, err
	}
	layoutResolver := func(engine string) (d2graph.LayoutGraph, error) {
		return d2dagrelayout.DefaultLayout, nil
	}
	renderOpts := &d2svg.RenderOpts{
		Pad:     go2.Pointer(int64(diagramPad)),
		ThemeID: &d2themescatalog.GrapeSoda.ID,
	}
	compileOpts := &d2lib.CompileOptions{
		LayoutResolver: layoutResolver,
		Ruler:          ruler,
	}

	diagram, _, err := d2lib.Compile(ctx, string(d2Diagram), compileOpts, renderOpts)
	if err != nil {
		return nil, 0, 0, err
	}

	out, err = d2svg.Render(diagram, renderOpts)
	if err != nil {
		return nil, 0, 0, err
	}

	// Before anything is done with the drawing. The PNG is rasterised by resvg,
	// which draws and does not execute, so that side no longer runs what the
	// diagram says -- but the SVG is uploaded whole, and served to whoever opens
	// the page. Checked in one place for both, because which output a run
	// produces is a flag, and a check that only guards one of them is a check
	// somebody turns off by accident.
	if err := checkDrawingIsSafe(out); err != nil {
		return nil, 0, 0, err
	}

	topLeft, bottomRight := diagram.BoundingBox()

	return out,
		bottomRight.X - topLeft.X + 2*diagramPad,
		bottomRight.Y - topLeft.Y + 2*diagramPad,
		nil
}

func ProcessD2(title string, d2Diagram []byte, scale float64) (attachment.Attachment, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), renderTimeout)
	ctx = d2log.WithDefault(ctx)
	defer cancel()

	out, width, height, err := renderSVG(ctx, d2Diagram)
	if err != nil {
		return attachment.Attachment{}, err
	}

	log.Debug().Msgf("Rendering: %q", title)

	pngBytes, err := rasterise(ctx, substituteEmbeddedFonts(out), scale)
	if err != nil {
		return attachment.Attachment{}, err
	}

	scaleAsBytes := make([]byte, 8)

	binary.LittleEndian.PutUint64(scaleAsBytes, math.Float64bits(scale))

	d2Bytes := append(d2Diagram, scaleAsBytes...)

	checkSum, err := attachment.GetChecksum(bytes.NewReader(d2Bytes))

	log.Debug().Msgf("Checksum: %q -> %s", title, checkSum)

	if err != nil {
		return attachment.Attachment{}, err
	}
	if title == "" {
		title = checkSum
	}

	fileName := title + ".png"

	return attachment.Attachment{
		ID:        "",
		Name:      title,
		Filename:  fileName,
		FileBytes: pngBytes,
		Checksum:  checkSum,
		Replace:   title,
		Width:     strconv.Itoa(width),
		Height:    strconv.Itoa(height),
	}, nil
}

// ProcessD2SVG publishes a diagram as the SVG it was drawn as, rather than a
// picture of it: one file that is sharp at any zoom and whose text stays text.
//
// scale multiplies the size the page displays it at. The file itself is the
// same drawing whatever that is, so unlike the PNG's the scale is not part of
// what identifies the attachment.
func ProcessD2SVG(title string, d2Diagram []byte, inputPath string, scale float64, bundleRemote bool) (attachment.Attachment, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), renderTimeout)
	ctx = d2log.WithDefault(ctx)
	defer cancel()

	out, width, height, err := renderSVG(ctx, d2Diagram)
	if err != nil {
		return attachment.Attachment{}, err
	}

	// A PNG carries what a diagram references by being a picture of it. An SVG
	// carries the reference itself, and Confluence serves the attachment from
	// its own host, where a path relative to the document resolves to nothing
	// and a remote image may be blocked. Both are inlined here, which is what
	// d2's own --bundle does.
	log.Debug().Msgf("Bundling what the diagram references: %q", title)

	// Every reference is judged before any of them is fetched, because the
	// bundler decides for itself what a name means: it reads an absolute path
	// as written, joins a relative one to wherever it was told the document is,
	// and fetches any URL with a client that follows redirects to anywhere at
	// all. None of that is reachable through its API, so the only place to say
	// no is here, before it is asked.
	local, remote, err := references(out, inputPath, bundleRemote)
	if err != nil {
		return attachment.Attachment{}, fmt.Errorf("diagram %q: %w", title, err)
	}

	if local {
		out, err = imgbundler.BundleLocal(ctx, bundleLogger{}, inputPath, out, false)
		if err != nil {
			return attachment.Attachment{}, err
		}
	}

	if remote {
		out, err = imgbundler.BundleRemote(ctx, bundleLogger{}, out, false)
		if err != nil {
			return attachment.Attachment{}, err
		}
	}

	// Taken over the drawing rather than over the source it was drawn from,
	// which is what the PNG side and mermaid do. Bundling is why: what the
	// diagram references is now inside the file, so an image that changed at
	// the other end of a URL changes the attachment without changing a line of
	// the source -- and a checksum over the source would call that unchanged
	// and leave the old drawing on the page.
	//
	// The cost is that anything else changing the bytes uploads them again. d2
	// draws the same diagram identically from one run to the next, which the
	// tests pin, so in practice that means a d2 upgrade that moves a line by a
	// pixel: one re-upload, of a drawing that did change.
	checkSum, err := attachment.GetChecksum(bytes.NewReader(out))
	log.Debug().Msgf("Checksum: %q -> %s", title, checkSum)

	if err != nil {
		return attachment.Attachment{}, err
	}

	if title == "" {
		title = checkSum
	}

	return attachment.Attachment{
		ID:        "",
		Name:      title,
		Filename:  title + ".svg",
		FileBytes: out,
		Checksum:  checkSum,
		Replace:   title,
		Width:     displayed(width, scale),
		Height:    displayed(height, scale),
	}, nil
}

// image matches the pictures d2 draws into an SVG, the same way d2's own
// bundler finds them.
var image = regexp.MustCompile(`<image href="([^"]+)"`)

// references reports what kinds of thing a drawing points at, and refuses the
// ones this run may not read.
//
// A file is held to the boundary an attachment is held to -- the document's own
// directory or the one mark is running in -- because a diagram naming a file is
// doing what a document does, and "../../../etc/id_rsa" reaches no further in
// one than the other. It needs a document on disk to be relative to: "-" is
// d2's way of writing standard input, and reading a name against the working
// directory instead is how a diagram compiled through the library reaches a
// file nobody offered it.
//
// A URL is refused unless this run asked for remote bundling, because fetching
// one is a request made by the document rather than by the person publishing
// it. The bundler's client follows redirects and declines nothing -- not
// loopback, not link-local, not a cloud metadata address -- and what comes back
// is published inside the drawing.
func references(svg []byte, inputPath string, bundleRemote bool) (local, remote bool, err error) {
	for _, match := range image.FindAllSubmatch(svg, -1) {
		href := stdhtml.UnescapeString(string(match[1]))

		// Already carried by the drawing rather than pointed at by it.
		if strings.HasPrefix(href, "data:") {
			continue
		}

		if parsed, parseErr := url.Parse(href); parseErr == nil &&
			(parsed.Scheme == "http" || parsed.Scheme == "https") {
			if !bundleRemote {
				return false, false, fmt.Errorf(
					"references %q, and fetching what a diagram names is off by default: "+
						"pass --d2-bundle-remote for documents whose diagrams you trust",
					href,
				)
			}

			remote = true

			continue
		}

		if inputPath == "" || inputPath == "-" {
			return false, false, fmt.Errorf(
				"references %q, which needs the document it was written in to be a file on disk",
				href,
			)
		}

		if err := attachment.CheckReadable(filepath.Dir(inputPath), href); err != nil {
			return false, false, fmt.Errorf("references %q: %w", href, err)
		}

		local = true
	}

	return local, remote, nil
}

// displayed reports the size the page should show a length at, as the whole
// number of pixels an attachment is measured in.
//
// Multiplied first and rounded once, since rounding before the scale scales a
// number that has already lost its fraction. Formatted as a float rather than
// converted to an int, because a length past what an int holds does not convert
// -- it becomes whatever the machine makes of it, and clamped to at least one
// pixel that would publish an enormous diagram one pixel wide.
//
// A scale that is not a number to multiply by leaves the length alone: run
// checks it, but ProcessD2SVG is exported and reachable without that.
func displayed(length int, scale float64) string {
	size := float64(length)
	if scale > 0 && !math.IsInf(scale, 0) {
		size *= scale
	}

	if size <= 0 || math.IsNaN(size) || math.IsInf(size, 0) {
		return ""
	}

	return strconv.FormatFloat(math.Max(1, math.Round(size)), 'f', -1, 64)
}

// executable are the elements that run or fetch something of their own, rather
// than drawing. d2 draws a |md | label as SVG of its own rather than passing
// the author's markup through, so nothing should reach these -- which is the
// reason to keep them: the check should not depend on a renderer continuing to
// be careful on mark's behalf.
var executable = map[string]bool{
	"script": true,
	"iframe": true,
	"object": true,
	"embed":  true,
}

// checkDrawingIsSafe refuses a rendered diagram that would do something rather
// than depict something.
//
// The SVG is uploaded to Confluence for other people's browsers to open, so a
// diagram in a pull request could wait there to be opened by a colleague. The
// PNG is rasterised rather than screenshotted now, so it no longer runs
// anything on the machine publishing -- the check stays over both because that
// is a property of the rasteriser, not a promise the diagram made.
//
// What a diagram has to say for itself is mostly drawn rather than passed
// through -- but not all of it: a d2 link is an address, and it goes into the
// drawing as the anchor it was written as, whether it names a page or names
// code.
//
// Refused rather than stripped. A diagram that asked to run something is not a
// diagram somebody drew by accident, and quietly publishing a different one
// than was written is its own kind of wrong.
//
// Read with the lenient HTML tokenizer rather than an XML parser: this is
// somebody else's markup being read for what it would do, and failing to parse
// it strictly is a way to fail on drawings that were fine.
func checkDrawingIsSafe(svg []byte) error {
	tokenizer := html.NewTokenizer(bytes.NewReader(svg))

	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			// Including io.EOF, which is how a document that held nothing
			// objectionable ends.
			return nil

		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttributes := tokenizer.TagName()
			if executable[strings.ToLower(string(name))] {
				return fmt.Errorf(
					"%w: it contains <%s>, which would run when the diagram is rendered or opened",
					ErrUnsafeDiagram, name,
				)
			}

			for hasAttributes {
				var key, value []byte

				key, value, hasAttributes = tokenizer.TagAttr()
				if err := checkAttribute(string(key), string(value)); err != nil {
					return err
				}
			}
		}
	}
}

// checkAttribute refuses the two ways an attribute runs something: by being an
// event handler, and by naming a URL that is code rather than a picture.
func checkAttribute(key, value string) error {
	key = strings.ToLower(strings.TrimSpace(key))

	if strings.HasPrefix(key, "on") {
		return fmt.Errorf(
			"%w: it sets %s, which would run when the diagram is rendered or opened",
			ErrUnsafeDiagram, key,
		)
	}

	if key != "href" && key != "src" && key != "xlink:href" {
		return nil
	}

	scheme, _, found := strings.Cut(strings.ToLower(strings.TrimSpace(value)), ":")
	if !found {
		// A relative reference, which the bundler decides about separately.
		return nil
	}

	switch scheme {
	case "http", "https", "mailto", "#":
		return nil

	case "data":
		// An image the bundler inlined. Anything else a data: URL can carry is
		// a document, which is to say a way to run something.
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:image/") {
			return nil
		}

		return fmt.Errorf("%w: it points at %.40s, which is not a picture", ErrUnsafeDiagram, value)

	default:
		return fmt.Errorf("%w: it points at a %s: address", ErrUnsafeDiagram, scheme)
	}
}

// bundleLogger hands what d2's bundler has to say to mark's own log.
type bundleLogger struct{}

func (bundleLogger) Debug(message string) { log.Debug().Msg(message) }
func (bundleLogger) Info(message string)  { log.Info().Msg(message) }
func (bundleLogger) Error(message string) { log.Error().Msg(message) }

// Cleanup shuts down the shared browser.
//
// This package no longer starts one -- it rasterises with resvg -- but mermaid
// and the math feature still can, and mark.go calls this alongside theirs. It
// stays a call into chrome/ rather than becoming a no-op so that a caller which
// only knows it renders diagrams does not have to know which package owns the
// browser.
func Cleanup() {
	chrome.Cleanup()
}

// resvgBinary rasterises the SVG that d2 has already drawn.
//
// d2 compiles, lays out and renders a diagram entirely in Go: by the time
// renderSVG returns, the drawing is finished. Chrome was only ever the thing
// that turned that drawing into a picture, which is a job a browser is a very
// large way to do -- it is half the weight of the image, and it has to be run
// with its sandbox disabled to work in a container at all.
//
// resvg does the same job in about 5 MB. mermaid still needs a real renderer,
// which is merman's job (--mermaid-engine=merman); this only replaces the
// screenshot.
const resvgBinary = "resvg"

// fontDirEnv names the directory resvg draws with, to the exclusion of the
// system's own fonts. Unset, resvg keeps its normal behaviour, which is what a
// developer running mark on a workstation wants.
const fontDirEnv = "MARK_FONT_DIR"

// rasterise turns the drawing into a PNG at the requested scale.
func rasterise(ctx context.Context, svg []byte, scale float64) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()

	// "-" reads the SVG from stdin, "-c" writes the PNG to stdout: nothing
	// touches the filesystem, so there is no temporary file to name or clean up.
	args := []string{"-", "-c", "--zoom", strconv.FormatFloat(scale, 'f', -1, 64)}

	// Where a font directory is named, it is the only one used. Otherwise resvg
	// draws with whatever fonts the machine happens to have -- which on a shared
	// CI agent is a set that other pipelines can change, and a diagram drawn with
	// a different font is a different diagram. The checksum is taken over the
	// source and the scale, not over the pixels, so a diagram that changed this
	// way is never re-uploaded: the page keeps the old picture and nothing says so.
	if dir := os.Getenv(fontDirEnv); dir != "" {
		args = append(args, "--skip-system-fonts", "--use-fonts-dir", dir)
	}

	cmd := exec.CommandContext(runCtx, resvgBinary, args...)
	cmd.Stdin = bytes.NewReader(svg)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("d2 rasterising timed out after %v", renderTimeout)
		}

		return nil, fmt.Errorf("%s failed: %w: %s", resvgBinary, err, bytes.TrimSpace(stderr.Bytes()))
	}

	if stdout.Len() == 0 {
		return nil, fmt.Errorf("%s produced no output: %s", resvgBinary, bytes.TrimSpace(stderr.Bytes()))
	}

	return stdout.Bytes(), nil
}

var (
	d2FontFace      = regexp.MustCompile(`(?s)@font-face\s*\{.*?\}`)
	d2FontReference = regexp.MustCompile(`font-family:\s*"?d2-\d+-font-([a-z-]+)"?`)
)

// substituteEmbeddedFonts points the drawing at fonts resvg can actually find.
//
// d2 subsets Source Sans Pro and Source Code Pro to the characters the diagram
// uses and embeds them in the SVG as base64 @font-face rules, under a family
// name generated per diagram ("d2-427536776-font-regular"). A browser reads
// that. resvg does not -- it reports
//
//	The @font-face rule is not supported. Skipped.
//	No match for 'd2-427536776-font-bold' font-family.
//
// and then draws the diagram with no text in it at all, exits 0 and writes a
// perfectly valid PNG. The only way to catch it is to look at the picture.
//
// So the rules are dropped and every reference is rewritten to the real family
// name. The image installs d2's own .ttf files, taken out of the Go module at
// build time, so the glyphs resvg draws are the ones d2 measured the text with
// and the layout it computed still fits.
func substituteEmbeddedFonts(svg []byte) []byte {
	svg = d2FontFace.ReplaceAll(svg, nil)

	return d2FontReference.ReplaceAllFunc(svg, func(match []byte) []byte {
		style := string(d2FontReference.FindSubmatch(match)[1])

		family := `"Source Sans Pro"`
		if strings.HasPrefix(style, "mono") {
			family = `"Source Code Pro"`
			style = strings.TrimPrefix(strings.TrimPrefix(style, "mono"), "-")
		}

		// The weight and the slant are part of the generated family name, and
		// have to become real CSS properties: one family holds all four faces.
		switch style {
		case "bold":
			return []byte(`font-family:` + family + `;font-weight:700;`)
		case "semibold":
			return []byte(`font-family:` + family + `;font-weight:600;`)
		case "italic":
			return []byte(`font-family:` + family + `;font-style:italic;`)
		default:
			return []byte(`font-family:` + family + `;`)
		}
	})
}

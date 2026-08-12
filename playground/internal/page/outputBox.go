package page

import (
	"go/scanner"
	"regexp"
	"strconv"
	"strings"

	"github.com/gopherjs/gopherjs/compiler/errlist"

	"github.com/gopherjs/gopherjs.github.io/playground/internal/bindings/react"
	"github.com/gopherjs/gopherjs.github.io/playground/internal/common"
)

// OutputBox creates a box React element for displaying output strings and errors.
func OutputBox(outputRef react.ValueRef[common.Output], onErrorLine react.Func) *react.Element {
	return react.CreateElement(outputBoxComponent, react.Props{}.
		Set(`outputRef`, outputRef).
		Set(`onErrorLine`, onErrorLine))
}

func outputBoxComponent(props react.Props) *react.Element {
	var (
		outputRef         = react.GetValueRef[common.Output](props, `outputRef`)
		onErrorLine       = props.GetFunc(`onErrorLine`)
		output, setOutput = react.UseState([]any{})
		outputBoxRef      = react.UseRef()
	)

	// Set the handle for the output so that other components call change output.
	react.UseImperativeHandle(outputRef, func() common.Output {
		return &outputImpl{setOutput: setOutput}
	}, []any{setOutput})

	// Add effect so that if there are no stderr's in the output, scroll to the bottom.
	react.UseLayoutEffect(func() {
		if !hasErrors(output) {
			outputBox := outputBoxRef.Current()
			scrollTop := outputBox.Get(`scrollHeight`).Int()
			outputBox.Set(`scrollTop`, scrollTop)
		}
	}, []any{output})

	// Update the error lines when the output changes.
	react.UseLayoutEffect(func() {
		errLines := map[string][]int{}
		for _, item := range output {
			itemMap := item.(map[string]any)
			if itemMap[typeKey] == stderrType {
				parseError(itemMap[contentKey], errLines)
			}
		}
		onErrorLine.Invoke(errLines)
	}, []any{output, onErrorLine})

	children := make([]react.Node, len(output))
	for i, item := range output {
		itemMap := item.(map[string]any)
		children[i] = outputLine(i, itemMap)
	}

	return react.Div(react.Props{}.
		Set(`id`, `output-box`).
		Set(`ref`, outputBoxRef),
		react.Div(react.Props{}.
			Set(`id`, `output-box-inner`),
			children...))
}

const (
	stderrType = `stderr`
	stdoutType = `stdout`
	systemType = `system`
	typeKey    = `type`
	contentKey = `content`
	styleKey   = `style`
)

// hasErrors determines if any output is an error.
func hasErrors(output []any) bool {
	for _, item := range output {
		if item.(map[string]any)[typeKey] == stderrType {
			return true
		}
	}
	return false
}

var errRegex = regexp.MustCompile(`^(\w+\.go):(\d+):`)

// parseError parses Go style stack frames from the given content to
// extract the file name and line number so that we can highlight the line
// number in the editor for the lines indicated in the error.
func parseError(content any, errLines map[string][]int) {
	if contentStr, ok := content.(string); ok {
		for _, line := range strings.Split(contentStr, "\n") {
			matches := errRegex.FindStringSubmatch(line)
			if len(matches) == 3 {
				if lineNo, err := strconv.Atoi(matches[2]); err == nil {
					fileName := matches[1]
					fileLines := errLines[fileName]
					fileLines = append(fileLines, lineNo)
					errLines[fileName] = fileLines
				}
			}
		}
	}
}

// outputLine creates a React element for a span of output content.
// The index is used to create a unique ID for the line so it should
// be the line's position in the output list.
func outputLine(index int, itemMap map[string]any) *react.Element {
	var (
		classType = itemMap[typeKey].(string)
		content   = itemMap[contentKey].(string)
		style     = itemMap[styleKey].(string)
	)
	props := react.Props{}
	if len(style) > 0 {
		props.Set(`style`, style)
	}
	return react.Span(props.
		Set(`key`, index).
		Set(`className`, classType),
		content)
}

func ungroupError(err error) []error {
	switch t := err.(type) {
	case scanner.ErrorList:
		errs := make([]error, len(t))
		for i, entry := range t {
			errs[i] = entry
		}
		return errs
	case errlist.ErrorList:
		return t
	default:
		return []error{err}
	}
}

var (
	ansiColors = []string{
		"#000", "#c00", "#0a0", "#a50", "#00c", "#c0c", "#0aa", "#ccc", // 0-7: standard
		"#555", "#f55", "#5f5", "#ff5", "#55f", "#f5f", "#5ff", "#fff", // 8-15: bright
	}
	ansiRegex = regexp.MustCompile(`\x1b\[([0-9;]*)m`)
)

func parseANSI(content string) []styledSegment {
	var segments []styledSegment
	fg, bg := ``, ``
	lastEnd := 0

	for _, match := range ansiRegex.FindAllStringSubmatchIndex(content, -1) {
		if match[0] > lastEnd {
			segments = append(segments, styledSegment{
				text:  content[lastEnd:match[0]],
				style: makeStyle(fg, bg),
			})
		}

		codes := content[match[2]:match[3]]
		for _, code := range strings.Split(codes, ";") {
			n, _ := strconv.Atoi(code)
			switch {
			case n == 0:
				fg, bg = ``, ``
			case n == 39:
				fg = ``
			case n == 49:
				bg = ``
			case n >= 30 && n <= 37:
				fg = ansiColors[n-30]
			case n >= 40 && n <= 47:
				bg = ansiColors[n-40]
			case n >= 90 && n <= 97:
				fg = ansiColors[n-90+8]
			case n >= 100 && n <= 107:
				bg = ansiColors[n-100+8]
			}
		}
		lastEnd = match[1]
	}

	if lastEnd < len(content) {
		segments = append(segments, styledSegment{
			text:  content[lastEnd:],
			style: makeStyle(fg, bg),
		})
	}
	return segments
}

func appendContent(items []any, itemTyp, content string, startNewLine bool) []any {
	// Check for a form feed ("\x0c") to clear the output.
	if index := strings.LastIndexByte(content, '\x0c'); index >= 0 {
		items = []any{}
		content = content[index+1:]
	}

	// If not stdout then just append as is.
	if itemTyp != stdoutType {
		return appendContentSegment(items, itemTyp, content, nil, startNewLine)
	}

	// Parse ANSI color codes for stdout content.
	segments := parseANSI(content)
	for i, seg := range segments {
		text := seg.text
		if text == `` {
			continue
		}

		// Try to append to the last item if type and style match.
		if maxItem := len(items) - 1; maxItem >= 0 {
			lastItem := items[maxItem].(map[string]any)
			if lastItem[typeKey] == itemTyp {
				lastStyle, _ := lastItem[styleKey].(map[string]string)
				if stylesEqual(lastStyle, seg.style) {
					lastItem[contentKey] = lastItem[contentKey].(string) + text
					continue
				}
			}
		}

		// Append new item with different type or style.
		newItem := map[string]any{typeKey: itemTyp, contentKey: text}
		if seg.style != nil {
			newItem[styleKey] = seg.style
		}
		items = append(items, newItem)
	}

	return items
}

func appendContentSegment(items []any, itemTyp, content string, style any, startNewLine bool) []any {
	// Attempt to append the content to a prior item if the type matches,
	// also prepend a new line to the content if needed and requested.
	if maxItem := len(items) - 1; maxItem >= 0 {
		lastItem := items[maxItem].(map[string]any)

		lastContent := lastItem[contentKey].(string)
		if startNewLine && !strings.HasSuffix(lastContent, "\n") {
			content = "\n" + content
		}

		if lastItem[typeKey] == itemTyp && lastItem[styleKey] == style {
			lastItem[contentKey] = lastContent + content
			return items
		}
	}

	// Append new type of content.
	item := map[string]any{typeKey: itemTyp, contentKey: content}
	if style != nil {
		item[styleKey] = style
	}
	return append(items, item)
}

func NoopOutput() common.Output {
	return (*outputImpl)(nil)
}

type outputImpl struct{ setOutput react.Func }

func (o *outputImpl) Clear() {
	if o == nil || o.setOutput == nil {
		return
	}
	o.setOutput.Invoke([]any{})
}

func (o *outputImpl) AddError(err error) {
	if o == nil || o.setOutput == nil {
		return
	}
	o.setOutput.Invoke(func(items []any) []any {
		content := ``
		for _, entry := range ungroupError(err) {
			text := entry.Error()
			content += text
			if !strings.HasSuffix(text, "\n") {
				content += "\n"
			}
		}
		return appendContent(items, stderrType, content, true)
	})
}

func (o *outputImpl) AppendOut(out string) {
	if o != nil && o.setOutput != nil {
		o.setOutput.Invoke(func(items []any) []any {
			return appendContent(items, stdoutType, out, false)
		})
	}
}

func (o *outputImpl) AppendErr(err string) {
	if o != nil && o.setOutput != nil {
		o.setOutput.Invoke(func(items []any) []any {
			return appendContent(items, stderrType, err, false)
		})
	}
}

func (o *outputImpl) AddSystem(sys string) {
	if o != nil && o.setOutput != nil {
		o.setOutput.Invoke(func(items []any) []any {
			return appendContent(items, systemType, sys+"\n", true)
		})
	}
}

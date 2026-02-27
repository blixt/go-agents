package toolresult

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flitsinc/go-llms/content"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type xmlResult struct {
	label   string
	content content.Content
	err     error
}

func (r *xmlResult) Label() string {
	return r.label
}

func (r *xmlResult) Content() content.Content {
	return r.content
}

func (r *xmlResult) Error() error {
	return r.err
}

func Success(toolName string, value any) llmtools.Result {
	return SuccessWithLabel(toolName, "Success", value)
}

func SuccessWithLabel(toolName, label string, value any) llmtools.Result {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Success"
	}
	return &xmlResult{
		label:   label,
		content: content.FromText(Render(toolName, value)),
	}
}

func SuccessWithContent(toolName, label string, extra content.Content, value any) llmtools.Result {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Success"
	}
	items := append(content.FromText(Render(toolName, value)), extra...)
	return &xmlResult{
		label:   label,
		content: items,
	}
}

func Error(toolName string, err error) llmtools.Result {
	return ErrorWithLabel(toolName, "", err)
}

func Errorf(toolName, format string, args ...any) llmtools.Result {
	return ErrorWithLabel(toolName, "", fmt.Errorf(format, args...))
}

func ErrorWithLabel(toolName, label string, err error) llmtools.Result {
	if err == nil {
		panic("toolresult: cannot create error result with nil error")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = fmt.Sprintf("Error: %s", err.Error())
	}
	return &xmlResult{
		label:   label,
		content: content.FromText(renderError(toolName, err.Error())),
		err:     err,
	}
}

func Render(toolName string, value any) string {
	root := tagName(toolName) + "_result"
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(root)
	b.WriteString(">\n")
	b.WriteString("<variable>$result1</variable>\n")
	b.WriteString("<value>\n")
	b.WriteString(escapeXMLText(formatValue(value)))
	b.WriteString("\n</value>\n")
	b.WriteString("</")
	b.WriteString(root)
	b.WriteString(">")
	return b.String()
}

func renderError(toolName, message string) string {
	root := tagName(toolName) + "_result"
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(root)
	b.WriteString(">\n")
	b.WriteString("<error>")
	b.WriteString(escapeXMLText(strings.TrimSpace(message)))
	b.WriteString("</error>\n")
	b.WriteString("</")
	b.WriteString(root)
	b.WriteString(">")
	return b.String()
}

func tagName(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return "tool"
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "tool"
	}
	if name[0] >= '0' && name[0] <= '9' {
		return "tool_" + name
	}
	return name
}

func formatValue(value any) string {
	if value == nil {
		return "null"
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return strconv.Quote(fmt.Sprintf("%v", value))
	}
	return string(data)
}

func escapeXMLText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

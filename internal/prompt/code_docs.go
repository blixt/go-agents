package prompt

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reExportFunc      = regexp.MustCompile(`^\s*export\s+(async\s+)?function\s+([A-Za-z0-9_]+)\s*\(([^)]*)\)`)
	reExportConstFunc = regexp.MustCompile(`^\s*export\s+const\s+([A-Za-z0-9_]+)\s*=\s*(async\s+)?function\s*\(([^)]*)\)`)
	reExportArrowFunc = regexp.MustCompile(`^\s*export\s+const\s+([A-Za-z0-9_]+)\s*=\s*(async\s*)?\(([^)]*)\)\s*=>`)
)

type codeDoc struct {
	File    string
	Name    string
	Args    string
	Comment string
}

func CollectCodeDocs(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", nil
	}

	var docs []codeDoc
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".ts" && ext != ".js" && ext != ".mjs" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		displayPath := filepath.ToSlash(filepath.Join(filepath.Base(dir), rel))
		fileDocs, err := parseCodeFile(path, displayPath)
		if err != nil {
			return err
		}
		docs = append(docs, fileDocs...)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(docs) == 0 {
		return "", nil
	}

	grouped := map[string][]codeDoc{}
	for _, doc := range docs {
		grouped[doc.File] = append(grouped[doc.File], doc)
	}

	var sb strings.Builder
	files := make([]string, 0, len(grouped))
	for file := range grouped {
		files = append(files, file)
	}
	sortStrings(files)
	for i, file := range files {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(file)
		sb.WriteString("\n")
		for _, doc := range grouped[file] {
			line := fmt.Sprintf("- %s(%s)", doc.Name, strings.TrimSpace(doc.Args))
			sb.WriteString(line)
			if doc.Comment != "" {
				sb.WriteString("\n  ")
				sb.WriteString(doc.Comment)
			}
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func parseCodeFile(path, displayPath string) ([]codeDoc, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var docs []codeDoc
	var commentLines []string
	inBlock := false

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if inBlock {
			endIdx := strings.Index(trimmed, "*/")
			if endIdx >= 0 {
				content := strings.TrimSpace(strings.TrimSuffix(trimmed[:endIdx], "*/"))
				if content != "" {
					commentLines = append(commentLines, strings.TrimPrefix(content, "*"))
				}
				inBlock = false
				continue
			}
			content := strings.TrimSpace(strings.TrimPrefix(trimmed, "*"))
			if content != "" {
				commentLines = append(commentLines, content)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/**") {
			inBlock = true
			content := strings.TrimSpace(strings.TrimPrefix(trimmed, "/**"))
			content = strings.TrimSuffix(content, "*/")
			if content != "" {
				commentLines = append(commentLines, strings.TrimPrefix(content, "*"))
				if strings.HasSuffix(trimmed, "*/") {
					inBlock = false
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			commentLines = append(commentLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "//")))
			continue
		}
		if trimmed == "" {
			continue
		}

		if name, args, ok := matchExport(trimmed); ok {
			docs = append(docs, codeDoc{
				File:    displayPath,
				Name:    name,
				Args:    args,
				Comment: strings.TrimSpace(strings.Join(commentLines, " ")),
			})
		}
		commentLines = nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

func matchExport(line string) (string, string, bool) {
	if matches := reExportFunc.FindStringSubmatch(line); len(matches) == 4 {
		return matches[2], matches[3], true
	}
	if matches := reExportConstFunc.FindStringSubmatch(line); len(matches) == 4 {
		return matches[1], matches[3], true
	}
	if matches := reExportArrowFunc.FindStringSubmatch(line); len(matches) == 4 {
		return matches[1], matches[3], true
	}
	return "", "", false
}

func sortStrings(values []string) {
	for i := 0; i < len(values)-1; i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

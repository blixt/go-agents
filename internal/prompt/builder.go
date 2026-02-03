package prompt

import (
	"sort"
	"strings"
)

type Block struct {
	ID       string
	Priority int
	Content  string
	CacheKey string
}

type Builder struct {
	blocks []Block
}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) Add(block Block) {
	if strings.TrimSpace(block.Content) == "" {
		return
	}
	b.blocks = append(b.blocks, block)
}

func (b *Builder) Build() string {
	if len(b.blocks) == 0 {
		return ""
	}
	blocks := make([]Block, len(b.blocks))
	copy(blocks, b.blocks)
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].Priority == blocks[j].Priority {
			return blocks[i].ID < blocks[j].ID
		}
		return blocks[i].Priority > blocks[j].Priority
	})

	var sb strings.Builder
	for i, block := range blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(block.Content)
	}
	return sb.String()
}

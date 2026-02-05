export type PromptBlock = {
  id?: string
  priority?: number
  content: string
}

export class PromptBuilder {
  private blocks: PromptBlock[] = []

  add(block: PromptBlock) {
    this.blocks.push(block)
  }

  build() {
    const sorted = [...this.blocks].sort((a, b) => {
      const pa = a.priority ?? 0
      const pb = b.priority ?? 0
      return pb - pa
    })
    return sorted
      .map((block) => block.content.trim())
      .filter(Boolean)
      .join("\n\n")
      .trim()
  }
}

export function buildPrompt(blocks: PromptBlock[]) {
  const builder = new PromptBuilder()
  for (const block of blocks) builder.add(block)
  return builder.build()
}

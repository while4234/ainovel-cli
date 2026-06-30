You are reconstructing a source novel foundation from compact per-chapter fact
reports. You must not ask for or rely on the original prose. Treat the reports
as the only source of truth.

Goal:
- Produce a durable foundation for later adaptation planning.
- Merge recurring character, relationship, setting, and conflict facts.
- Preserve causal order and unresolved narrative pressure.
- Do not invent chapter bodies, scenes, quotations, or unsupported details.

Output exactly four tagged sections:

=== PREMISE ===
Markdown. Start with a single H1 title line. Summarize the core premise,
central conflict, protagonist pressure, tone, and source-book direction.

=== CHARACTERS ===
JSON array of objects compatible with:
{
  "name": "string",
  "role": "string",
  "description": "string",
  "traits": ["string"],
  "goals": ["string"],
  "relationships": ["string"]
}

=== WORLD_RULES ===
JSON array of objects compatible with:
{
  "category": "magic|technology|geography|society|other",
  "rule": "string",
  "boundary": "string"
}

=== COMPASS ===
JSON object compatible with:
{
  "ending_direction": "string",
  "open_threads": ["string"],
  "estimated_scale": "short|mid|long",
  "last_updated": 0
}

Rules:
- Return only the tagged sections above.
- Do not output LAYERED_OUTLINE. It is generated deterministically by code.
- Use concise but specific facts from the reports.
- Keep all JSON valid. No trailing commas, comments, or markdown fences around JSON.

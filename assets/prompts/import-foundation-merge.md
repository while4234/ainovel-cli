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
  "id": "stable source character ID; distinguish same-name people",
  "name": "string",
  "aliases": ["string"],
  "role": "string",
  "description": "string",
  "arc": "changes already evidenced in the reports; never invent a future endpoint",
  "traits": ["string"],
  "tier": "core|important|secondary|decorative",
  "faction": "string",
  "goal": "string",
  "motivation": "string",
  "conflict": "string",
  "voice": "string",
  "constraints": ["string"],
  "contrast_details": [{"surface":"string","depth":"string"}],
  "key_backstory": [{"event":"string","impact":"string"}],
  "initial_state": {
    "identity":"string","situation":"string","emotion":"string",
    "resources":["string"],"relationships":"string"
  },
  "knowledge_boundary": {
    "known":["string"],"unknown":["string"],
    "misconceptions":["string"],"forbidden":["string"]
  },
  "notes": "uncertainty or evidence boundary"
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
- Use only the Character fields listed above. Never emit legacy `goals` or
  top-level character `relationships`; chapter relationship evidence remains
  in the reports and later planned relationships use their own schema.
- Merge aliases into one character only when report evidence supports the
  identity. Keep same-name different people separate with stable IDs. Preserve
  renames as aliases and preserve chapter ranges in compact notes when useful.
- Leave unsupported fields empty and mark uncertainty in notes. Never invent a
  complete growth endpoint not present in the reports.
- Use concise but specific facts from the reports.
- Keep all JSON valid. No trailing commas, comments, or markdown fences around JSON.

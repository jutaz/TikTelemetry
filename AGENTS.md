<!-- symtether:begin v1 (managed by `symtether init` — do not edit) -->
## Code references
Links like `[x](path/file.ts#sym:Class.method)` point at a symbol in that file.
- Resolve: grep the symbol name in the file; read the surrounding code.
- After renaming/moving symbols: run `npx symtether check`, repair refs (`npx symtether fix`).
- When writing docs/skills, prefer `#sym:` refs over line numbers or pasted snippets.
Spec: https://symtether.dev/spec
<!-- symtether:end -->

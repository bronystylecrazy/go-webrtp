# Backend

## Guideline

- If any variable should apply in the name, it will have `#variableName#` in the name, e.g., `#entityName#IdRequest`

## Practice

- During implementation, if you find any code that can be reused, please extract it as a public function and reuse it in
  other places. Do not copy and paste the code.
- Keep single source of truth, do not duplicate the logic or declaration in multiple places. Find the best place to put
  the logic or declaration, and reuse it in other places. If it's not obvious where to put the logic or declaration,
  please discuss by prompting for answers instead of making a decision by yourself.
- If you see any files edited by me, see what practice I followed, and follow the same practice, do not undo by changes.

## Code

- Only comment as `// * short lowercase description`, do not add additional comment when editing.
- Use `r` as pointer receiver name for all struct, e.g., `func (r *Service) UserCreate(...)`
- For functions naming, use `Entity` + `Action` format, e.g., `UserCreate`, and always be public functions. The sub or
  helper function must also be public functions with same prefix as the main function, e.g., `UserCreateValidate`,
  except for receiver functions name that can use action based on its struct name.
- Use standard functions as basis, e.g., `url.JoinPath`, `filepath.Join`, `math.Abs`. Do not implement logic yourself.
- Use camel case for all yaml and json tags.
- Force declarations to camel or title case regardless of abbreviation, e.g., `AnnexbToAvcc` instead of `AnnexBToAVCC`.
- Use pointer for any struct, both return value, parameter and array element,
  e.g., `func(ctx context.Context, req *UserRequest) (*Response)`, `make([]*webrtp.StreamStats, 0)`, `[]*User`, 
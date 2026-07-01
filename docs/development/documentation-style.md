# Documentation style


Use the [Google developer documentation style guide](https://developers.google.com/style)
as the baseline for project documentation. Project-specific terms and safety
requirements take priority when they conflict with the general guide.

## Write for the reader

- Address the reader as `you` when the reader performs the task.
- Use active voice when it makes the actor clear.
- Use present tense for general behavior.
- Put conditions before instructions.
- Avoid idioms, humor, and culturally specific wording.

## Structure pages consistently

- Use sentence case for page titles and headings.
- Give each page one `h1` heading.
- Use task-based headings for procedures, such as `Configure AWS auth`.
- Use noun-phrase headings for concepts, such as `Provider capabilities`.
- Use numbered lists only for ordered procedures.

## Format technical content

- Wrap commands, paths, fields, statuses, and literal values in backticks.
- Use descriptive link text instead of `here` or raw URLs.
- Keep examples copyable unless the text explicitly labels them as fragments.
- Prefer short paragraphs and parallel list items.

## Keep the information architecture clean

- Put onboarding and first-use workflows in `docs/getting-started/`.
- Put task workflows in `docs/guides/`.
- Put destination setup in `docs/providers/`.
- Put operational procedures in `docs/operations/`.
- Put threat model and security controls in `docs/security/`.
- Put API and compatibility material in `docs/reference/`.
- Put implementation contracts and contributor guidance in `docs/development/`.

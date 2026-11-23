# Writing guide

Follow this guide when you write or review documentation for this project: the README, the docs site in `docs/`, and any other prose a user reads. Read it before you start a writing task, not after.

Unless this guide says otherwise, use the [Chicago Manual of Style](https://www.chicagomanualofstyle.org/) and the Oxford English Dictionary. When you describe technical content, use [Simple Technical English](https://www.asd-ste100.org/).

## The voice

sandbar docs are plain, direct, and opinionated. They sound like a friendly colleague, or mentor, who built the tool, knows where it falls short, and will tell you what they would do.

The reader is a developer in the middle of a task, or a coding agent reading on their behalf. Both want the answer first and can't use a joke. The reader is new to sandbar but technically adept and has probably used similar things before.

### Rules

1. Lead with the answer or the action. Background comes after, if at all.
2. Say "you" and use the present tense. "sandbar clones the base image," not "the base image will be cloned."
3. Be opinionated. Recommend, and give the reason. When the reader has to choose, say "we recommend X because Y." Recommendations belong in how-to guides and explanation. Reference stays neutral.
4. State limits plainly. Say what sandbar doesn't do and what it doesn't protect against. Don't hedge. Don't over explain which can be distracting.
5. Claim only what is true. Drop "simply," "just," and "easy." Make no security promise the [Security model](../background/security-model.md) page doesn't back.
6. Prefer literal to figurative. No metaphors, idioms, or irony in procedures. An agent or a non-native reader may take them at face value.
7. Use one term per thing. If it's a VM on one page, it's a VM on every page.
8. Explain why when a step is surprising. One sentence is enough. Link to an explanation page for the rest.

### Voice by page type

The docs follow Diátaxis, and each of its four page types has its own register. Wit belongs only on pages people read once and outside a task.

| Page type | Voice |
| --- | --- |
| Tutorial | Guiding and encouraging. The reader is learning, so keep explanation to one sentence and link out. No jokes. |
| How-to guide | Imperative and plain, for a reader who already knows the basics. Recommend where there is a choice. Troubleshooting belongs here: stay empathetic, because the reader is stuck. |
| Reference | Neutral. Facts only, with no opinion and no asides. |
| Explanation | Opinion and perspective live here. Say why sandbar works the way it does and what the trade-offs are. |
| Homepage, Why sandbar | Personality welcome. Dry beats jokey. Still accurate. |
| Release notes | One light line at the top is fine. |

Security content follows its page type, with one extra rule: no humor, and no reassurance beyond the facts.

### Before and after

These three rewrites show the voice. The "before" lines are invented for illustration, not quoted from the current docs.

| Before | After | Rule |
| --- | --- | --- |
| In order to facilitate rapid provisioning, a base image is utilized which is constructed a single time and subsequently cloned. | sandbar builds a base image once. Each new VM is a clone of it, so it starts in seconds. | 1, 2 |
| Let your agent go wild! It's basically impossible for it to break anything. | The agent runs inside the VM, not on your machine. See the security model for what that does and doesn't isolate. | 4, 5 |
| VMs can be run locally, over SSH, or on Proxmox. | Start with local VMs. Move to a server or Proxmox when [maintainers' reason]. | 3 |

The third "after" needs a real recommendation from the maintainers. The bracket is a placeholder, not advice.

## Page structure by type

Each page should follow the structure outlined below depending on its type. This isn't a strict rule, but helps to keep things consistent.

### Rules for every page

- The H1 matches the page's title in the `nav:` of `mkdocs.yml`.
- Start with a hook. The first paragraph says what the page is for.
- Spell the shared section names the same way on every page: *Before you start*, *Next steps* and *Related*.
- Keep the list of links at the end of a page short. Five or fewer is a good rule of thumb.
- How-to guide titles are imperative ("Open a shell"), not "How to open a shell".

### Tutorial structure

1. What the reader will have at the end, in one or two sentences.
2. *Before you start*: the prerequisites.
3. Numbered steps. Each step ends in something the reader can see. Show the expected output, and add a warning where a step commonly fails.
4. *What you built*: a short summary.
5. *Next steps*: two or three links.

### How-to guide structure

1. One sentence on what the page does and when you need it.
2. *Before you start*, only if there are prerequisites.
3. The steps. Put variants in subsections.
4. *If something goes wrong* (optional): link to *Troubleshooting*.
5. *Related*: links.

### Reference structure

1. One sentence on what the page covers.
2. The entries, in the same order every time and following the product's own structure. Give each entry the same fields.

No other introduction, no conclusion and no next steps. These pages should be easy to scan and written for frequent re-reading.

### Explanation structure

1. The question the page answers.
2. Sections that build on each other.
3. *Trade-offs*.
4. *Related*: links.

## Who the docs are for

The docs site is for people who **use** sandbar. Put how sandbar is built (package names, internal seams, why an implementation changed) in `AGENTS.md` or the *Contributing* section, not in user pages. Keep an implementation detail on a user page only when the reader needs it to make a decision or fix a problem, and then say why it matters to them.

Order content by what a reader does first, then what they do often, then what they do rarely.

## Steps for agents to follow when reviewing or drafting content

### 1st pass: full content read

- Understand the document's **main thesis** and **target audience**.
- Identify the **technical domain**.
- Identify the **document type**. Use the [Diátaxis framework](https://diataxis.fr/start-here/) (tutorial, how-to guide, reference, explanation) to identify the type and audience, and let that inform the composition and the voice (see *Voice by page type*). Do not mix types on one page.

### 2nd pass: structure analysis

- Check that the first sentence gives the answer or the action (rule 1).
- Assess **narrative flow**: does the content progress logically?
- Identify **missing elements** that disrupt comprehension.
- Check for **structural issues**, such as unclear transitions or gaps in explanation.

### 3rd pass: technical review

- Verify the **accuracy of claims** and make sure all explanations are **technically correct**. Check claims against the code, not against other docs. Check each security claim against the [Security model](../background/security-model.md) page (rule 5).
- Validate **code examples** for correctness, clarity and relevance. Run commands where you can.
- Define **undefined terms** or link to an explanation.
- Think about the skills someone needs to do similar tasks, rather than focusing on one narrowly defined task.
- Where the reader has to choose, recommend one option and give the reason (rule 3). For example:

  ```diff
  - Choose a module that can resize images.
  + Use the Image Cache module to resize images.
  ```

### 4th pass: language optimization

Check for consistency against the word list (rule 7):

| Use | Not |
| --- | --- |
| **sandbar** or `sandbar` | Sandbar, `sand` |
| **Drupal** | drupal |
| **Lullabot** | |
| **JavaScript** | javascript, JS |
| **email** | e-mail |
| *(leave out)* | simply, just, easy (rule 5) |

Then apply these style rules:

- Put **UI strings in italics**, for example *Configuration* > *System settings*.
- Use **active voice**, not passive (rule 2).
- Be **specific**. Avoid generalizations.
- Keep language **concise**. Remove filler words and redundant phrases.
- Define jargon and acronyms at first use.
- Write **inclusively and non-violently**.
- Use **sentence case** for headings and list items.
- Improve **readability**:
  - Vary sentence and paragraph length.
  - Use **transitions** for logical flow.
  - Break up walls of text with lists, headings or visuals.
- **Accessibility**: give images alt text, keep a structured heading hierarchy and write meaningful link text.
- **Internationalization**: avoid idioms, cultural references and slang (rule 6).
- **Content aging**: remove outdated references and make sure version information is still correct.

## Review checklist

Ask these seven questions of any docs PR.

- Is the page one Diátaxis type, or is it mixing two?
- Does the page follow its type's structure in *Page structure by type*, and the rules for every page?
- Does the first sentence give the answer or the action?
- Can every sentence be taken literally?
- Is each claim true, and is each security claim backed by the [Security model](../background/security-model.md) page?
- Where the reader has to choose, is there a recommendation with a reason?
- Would any line annoy on the tenth read? Cut it.

A prose linter such as Vale can enforce the banned words in rule 5 and the term list in rule 7, so review time goes to the rest.

## Before you open a PR

- Don't hard-wrap Markdown. Put each paragraph or list item on one line, so a reworded sentence changes one line in the diff.
- Run `uvx --with-requirements docs/requirements.txt mkdocs build --strict`.
- Keep a PR to one page, or one small group of pages that must move together (for example, a page split and the `nav:` change it needs).

# widget

The contract a widget file is written against. Every attribute the file
may carry is declared under the rule that admits it.

## #1 The file

- **#2 A widget names itself and lists its parts.** Prose.
  - key `name`: string, required
  - key `kind`: "widget", required
  - key `parts`: map, optional
  - key `parts.*`: map
  - key `parts.*.size`: integer, optional
  - key `parts.*.mode`: "eventual" | "transactional", optional
  - key `labels`: list, optional

## #3 Steps

- key `extra`: boolean, optional

- **#4 A step is an entry, or a group of entries run together.** Prose.
  - key `steps`: list, required
  - key `steps[]`: $entry | list
  - key `steps[][]`: $entry
- **#5 An entry is a status, and its kind decides what else it carries.**
  - key `$entry`: map
  - key `$entry.status`: "queue" | "plain" | "hold", required
  - key `$entry.flow`: string, required, when status is "queue"
  - key `$entry.blocks`: list, optional, when status is "queue"
  - key `$entry.fills`: list, optional, when status is "plain"
  - key `$entry.owner`: string, required, when status is "queue" | "hold"
  - key `$entry.until`: string, optional, when status is "hold" | "queue"
  - key `$entry.note`: any, optional

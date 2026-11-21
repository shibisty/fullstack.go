// An island: rendered by Go as an empty <fullstack-island>, mounted here in
// the browser with the notes the page passed ({{island "NoteSearch" ...}}).
import { useState } from "preact/hooks";

type Note = { id: number; text: string };

export default function NoteSearch(props: { notes: Note[] }) {
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const shown = props.notes.filter((n) => n.text.toLowerCase().includes(q));
  return (
    <div class="note-search">
      <input
        type="search"
        placeholder="Filter notes"
        value={query}
        onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
      />
      <ul>
        {shown.map((n) => (
          <li key={n.id}>
            {n.text} <a href={`/api/notes/${n.id}`}>json</a>
          </li>
        ))}
        {shown.length === 0 && <li>No notes match.</li>}
      </ul>
    </div>
  );
}

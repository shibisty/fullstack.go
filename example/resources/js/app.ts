// Built by @fullstack/bundler (esbuild in the Go process): no Node.js needed.
type Note = { id: number; text: string };

async function countNotes(el: HTMLElement): Promise<void> {
  let notes: Note[];
  try {
    const res = await fetch("/api/notes", { headers: { Accept: "application/json" } });
    notes = await res.json();
  } catch {
    el.textContent = "offline";
    return;
  }
  el.textContent = `${notes.length} note${notes.length === 1 ? "" : "s"} (via the API)`;
}

// "fullstack:load" comes after the first load and after every page swap
// (bundler.Options.Navigate), so the page's elements are found each time.
document.addEventListener("fullstack:load", () => {
  document.querySelectorAll<HTMLElement>("[data-note-count]").forEach((el) => {
    void countNotes(el);
  });
});

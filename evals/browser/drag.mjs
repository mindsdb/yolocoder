// Select a grab point without interacting, then perform one real pointer drag.
// The target and subsequent acceptance assertions remain the caller's concern.
export async function dragFromGrabArea(article, target, title) {
  const deadline = Date.now() + 3000;
  const remaining = () => {
    const ms = deadline - Date.now();
    if (ms <= 0) throw new Error('No usable card grab area within the 3000ms drag budget');
    return ms;
  };
  await article.scrollIntoViewIfNeeded({ timeout: remaining() });
  const sourcePosition = await article.evaluate((source, preferredText) => {
    const box = source.getBoundingClientRect();
    const style = getComputedStyle(source);
    const origin = { x: box.x + parseFloat(style.borderLeftWidth), y: box.y + parseFloat(style.borderTopWidth) };
    const interactive = 'input,select,textarea,button,a,label,summary,iframe,object,embed,audio,video,[contenteditable]:not([contenteditable="false"]),[role="button"],[role="link"],[role="textbox"],[role="combobox"],[role="listbox"],[role="option"],[role="checkbox"],[role="radio"],[role="switch"],[role="slider"],[role="spinbutton"],[role="menuitem"],[role="tab"]';
    const usable = (x, y) => {
      x = Math.floor(x); y = Math.floor(y);
      if (x <= origin.x || y <= origin.y || x >= box.right || y >= box.bottom || x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) return null;
      const hit = document.elementFromPoint(x, y);
      if (!hit || !source.contains(hit) || hit.closest(interactive) || hit.isContentEditable) return null;
      for (let node = hit; node; node = node.parentElement) {
        const css = getComputedStyle(node);
        if (css.visibility !== 'visible' || css.display === 'none' || Number(css.opacity) === 0) return null;
      }
      return { x: x - origin.x, y: y - origin.y };
    };
    // A visible title is a natural grab area even when the card center is a select.
    // Bound traversal; unusual markup can still use the generic interior samples.
    const normalize = text => text.trim().replace(/\s+/g, ' ');
    const walker = document.createTreeWalker(source, NodeFilter.SHOW_TEXT);
    for (let n = 0, node; n < 256 && (node = walker.nextNode()); n++) {
      if (!preferredText || normalize(node.textContent) !== normalize(preferredText)) continue;
      const range = document.createRange(); range.selectNodeContents(node);
      for (const rect of Array.from(range.getClientRects()).slice(0, 16)) {
        if (rect.width <= 0 || rect.height <= 0) continue;
        const point = usable(rect.x + rect.width / 2, rect.y + rect.height / 2);
        if (point) return point;
      }
    }
    for (const fy of [0.5, 0.2, 0.8]) for (const fx of [0.5, 0.2, 0.8]) {
      const point = usable(box.x + box.width * fx, box.y + box.height * fy);
      if (point) return point;
    }
    return null;
  }, title, { timeout: remaining() });
  if (!sourcePosition) throw new Error('No unobstructed noninteractive grab area in card');
  await article.dragTo(target, { sourcePosition, timeout: remaining() });
}

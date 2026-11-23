import { h } from "./vendor/preact.mjs";
import htm from "./vendor/htm.mjs";

// html is the htm template tag bound to Preact's hyperscript.
export const html = htm.bind(h);

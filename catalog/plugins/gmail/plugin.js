// @ts-check
// Gmail, read-only, through the Gmail REST API.
//
// The token is never in here. The manifest binds a credential to gmail.googleapis.com and the host
// stamps it in at the boundary — this code only writes URLs, which is also why the credential cannot
// leak into a result: it never passes through the guest.
//
// Everything is GET, so every call goes through http_read and the cage is `uses: ["http_read"]`.
// Sending mail is deliberately absent: a send is gated on the HOST, and one approval for
// gmail.googleapis.com would then cover every future message to every recipient. That belongs in a
// tool that can gate per recipient.

const API = "https://gmail.googleapis.com/gmail/v1/users/me";
const MAX_RESULTS = 25;
const BODY_LIMIT = 20000; // one mail's plain text, past which nobody reads it and the context pays

/**
 * get fetches one API path and returns the parsed JSON.
 * @param {string} path
 * @returns {Promise<any>}
 */
async function get(path) {
  const r = await fetch(API + path);
  if (r.status === 401 || r.status === 403) {
    throw new Error(
      "Gmail refused the request (" + r.status + "). The account is not connected, or its scopes " +
      "do not cover this. Connect it with: nocturn auth gmail"
    );
  }
  if (!r.ok) throw new Error("Gmail request failed: " + r.status + " " + r.statusText);
  return await r.json();
}

/**
 * header pulls one header value out of a message payload, case-insensitively.
 * @param {any} payload
 * @param {string} name
 * @returns {string}
 */
function header(payload, name) {
  const wanted = name.toLowerCase();
  const headers = (payload && payload.headers) || [];
  for (const h of headers) {
    if (String(h.name).toLowerCase() === wanted) return String(h.value);
  }
  return "";
}

/**
 * plainText walks a message payload for its text/plain part, falling back to stripped HTML.
 *
 * A Gmail body is a tree: simple mails carry it at the root, multipart/alternative has plain beside
 * HTML, and multipart/mixed nests both under attachments. Taking the FIRST text/plain found depth
 * first is what matches how a mail client renders it.
 * @param {any} part
 * @returns {string}
 */
function plainText(part) {
  if (!part) return "";
  const mime = String(part.mimeType || "");
  const data = part.body && part.body.data;
  if (mime === "text/plain" && data) return decode(data);
  for (const child of part.parts || []) {
    const found = plainText(child);
    if (found) return found;
  }
  if (mime === "text/html" && data) return stripTags(decode(data));
  return "";
}

/**
 * decode turns Gmail's base64url body into text.
 * @param {string} data
 * @returns {string}
 */
function decode(data) {
  return Buffer.from(String(data), "base64url").toString("utf8");
}

/**
 * stripTags reduces HTML to readable text. Crude on purpose — the alternative is a parser in the
 * guest, and the plain part is what nearly every mail carries anyway.
 * @param {string} html
 * @returns {string}
 */
function stripTags(html) {
  return html
    .replace(/<(script|style)[\s\S]*?<\/\1>/gi, "")
    .replace(/<br\s*\/?>/gi, "\n")
    .replace(/<\/p>/gi, "\n\n")
    .replace(/<[^>]+>/g, "")
    .replace(/&nbsp;/g, " ")
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

globalThis.plugin = {
  tools: {
    /**
     * search lists matching messages with just enough of each to decide which to open.
     * @param {{query: string, limit?: number}} args
     */
    search: async (args) => {
      const query = String(args.query || "").trim();
      if (!query) throw new Error("a query is required, e.g. is:unread or from:anna newer_than:7d");
      const limit = Math.min(Math.max(Number(args.limit) || 10, 1), MAX_RESULTS);

      const list = await get(
        "/messages?maxResults=" + limit + "&q=" + encodeURIComponent(query)
      );
      const ids = (list.messages || []).map((m) => String(m.id));
      if (ids.length === 0) return "No messages match " + JSON.stringify(query) + ".";

      // One metadata request per hit rather than one full fetch: the headers and the snippet are
      // what a person picks from, and pulling whole bodies here would spend the context on mail
      // nobody asked to read.
      const rows = [];
      for (const id of ids) {
        const m = await get(
          "/messages/" + encodeURIComponent(id) +
          "?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date"
        );
        rows.push({
          id: id,
          from: header(m.payload, "From"),
          subject: header(m.payload, "Subject"),
          date: header(m.payload, "Date"),
          unread: (m.labelIds || []).indexOf("UNREAD") >= 0,
          snippet: String(m.snippet || ""),
        });
      }
      return JSON.stringify({ query: query, count: rows.length, messages: rows }, null, 1);
    },

    /**
     * read returns one message whole: its headers and its plain text.
     * @param {{id: string}} args
     */
    read: async (args) => {
      const id = String(args.id || "").trim();
      if (!id) throw new Error("a message id is required (take one from gmail_search)");

      const m = await get("/messages/" + encodeURIComponent(id) + "?format=full");
      let body = plainText(m.payload).trim();
      let truncated = false;
      if (body.length > BODY_LIMIT) {
        body = body.slice(0, BODY_LIMIT);
        truncated = true;
      }
      return JSON.stringify(
        {
          id: id,
          from: header(m.payload, "From"),
          to: header(m.payload, "To"),
          cc: header(m.payload, "Cc"),
          subject: header(m.payload, "Subject"),
          date: header(m.payload, "Date"),
          labels: m.labelIds || [],
          body: body || "(no readable text part)",
          truncated: truncated,
        },
        null,
        1
      );
    },

    /**
     * labels lists the mailboxes and labels, so a search can be scoped to one by name.
     */
    labels: async () => {
      const out = await get("/labels");
      const labels = (out.labels || []).map((l) => ({
        id: String(l.id),
        name: String(l.name),
        type: String(l.type || ""),
      }));
      return JSON.stringify({ count: labels.length, labels: labels }, null, 1);
    },
  },
};

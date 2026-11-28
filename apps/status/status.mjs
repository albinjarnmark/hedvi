const maxAge = 20 * 60 * 1000;
const expected = ["website", "dashboard", "api"];

export function classify(snapshot, now = Date.now()) {
  const when = Date.parse(snapshot?.checked_at);
  if (!Number.isFinite(when) || now - when > maxAge || when - now > 60000) return "unknown";
  if (!Array.isArray(snapshot?.components) || snapshot.components.length !== expected.length) return "unknown";
  if (!snapshot.components.every(c => c && typeof c === "object")) return "unknown";
  if (!expected.every(id => snapshot.components.filter(c => c.id === id).length === 1)) return "unknown";
  if (!snapshot.components.every(c => ["passing", "failing"].includes(c.status))) return "unknown";
  if (!["none", "active"].includes(snapshot.incident?.state)) return "unknown";
  if (snapshot.incident.state === "active" && !["title", "message", "updated_at"].every(k => typeof snapshot.incident[k] === "string" && snapshot.incident[k].trim())) return "unknown";
  return snapshot.components.some(c => c.status === "failing") || snapshot.incident?.state === "active" ? "issue" : "passing";
}

if (typeof document !== "undefined") {
  let latest = null;
  const labels = {website: ["Website", "Public website and documentation entry point"], dashboard: ["Dashboard", "Sign-in page availability"], api: ["Email API", "API and database readiness"]};
  function render(snapshot) {
    const state = classify(snapshot);
    document.getElementById("summary").textContent = state === "passing" ? "Latest checks are passing" : state === "issue" ? "A service issue needs attention" : "Current status is unavailable";
    document.getElementById("summary-dot").className = `dot ${state === "passing" ? "good" : state === "issue" ? "bad" : ""}`;
    const when = Date.parse(snapshot?.checked_at);
    document.getElementById("checked").textContent = Number.isFinite(when) ? `Last checked ${new Date(when).toLocaleString()}${state === "unknown" ? ". These results are not current." : "."}` : "We could not retrieve a recent check. Please try again shortly.";
    const rows = expected.map(id => {
      const [name, detail] = labels[id], component = Array.isArray(snapshot?.components) ? snapshot.components.find(c => c?.id === id) : undefined;
      const row = document.createElement("div"); row.className = "service";
      const info = document.createElement("div"), title = document.createElement("h3"), description = document.createElement("p"), badge = document.createElement("span");
      title.textContent = name; description.textContent = detail;
      const current = state !== "unknown";
      badge.className = `badge ${current ? component?.status === "passing" ? "good" : "bad" : ""}`;
      badge.textContent = current ? component?.status === "passing" ? "Passing" : "Check failed" : "Unknown";
      info.append(title, description); row.append(info, badge); return row;
    });
    document.getElementById("components").replaceChildren(...rows);
    const incident = snapshot?.incident;
    document.getElementById("incident").hidden = incident?.state !== "active";
    if (incident?.state === "active") {
      document.getElementById("incident-title").textContent = incident.title;
      document.getElementById("incident-message").textContent = incident.message;
      const updated = Date.parse(incident.updated_at);
      document.getElementById("incident-time").textContent = Number.isFinite(updated) ? `Incident update: ${new Date(updated).toLocaleString()}` : "Contact support for the latest incident update.";
    }
  }
  async function refresh() {
    try {
      const response = await fetch(`./status.json?t=${Date.now()}`, {cache: "no-store", credentials: "omit", signal: AbortSignal.timeout(10000)});
      if (!response.ok) throw new Error("Status unavailable");
      latest = await response.json();
    } catch { latest = null; }
    render(latest);
  }
  refresh();
  setInterval(refresh, 60000);
  setInterval(() => render(latest), 15000);
}

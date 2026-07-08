(function () {
  function fmtTokens(n) {
    n = n || 0;
    if (n >= 1000) return Math.round(n / 1000) + "k";
    return "" + n;
  }

  var refresh = document.getElementById("refresh");
  var dispatch = document.getElementById("dispatch");
  var cancel = document.getElementById("cancel");
  var bar = document.getElementById("bar");
  var status = document.getElementById("status");
  var tokens = document.getElementById("tokens");
  var lanes = document.getElementById("lanes");

  function setBusy(busy) {
    refresh.disabled = busy;
    dispatch.disabled = busy;
    cancel.hidden = !busy;
  }

  function reloadLanes() {
    fetch("/lanes")
      .then(function (r) { return r.text(); })
      .then(function (html) { lanes.innerHTML = html; applyAllSorts(); });
  }

  // Per-section sort cycle. The server always renders default order, so we
  // capture it as data-i on (re)load and re-apply the active mode after each
  // /lanes swap. Modes are keyed by section id and persist across reloads.
  var SORT_CYCLE = ["default", "oldest", "recent"];
  var SORT_LABEL = { default: "↕ default", oldest: "↕ oldest in state", recent: "↕ recently updated" };
  var sortModes = {};

  function tagOrder() {
    document.querySelectorAll(".lane-rows").forEach(function (g) {
      var i = 0;
      g.querySelectorAll(":scope > .row").forEach(function (row) { row.dataset.i = i++; });
    });
  }

  function applySort(section) {
    var mode = sortModes[section.id] || "default";
    var btn = section.querySelector(".sort-btn");
    if (btn) {
      btn.textContent = SORT_LABEL[mode];
      btn.classList.toggle("active", mode !== "default");
    }
    section.querySelectorAll(".lane-rows").forEach(function (g) {
      var rows = Array.prototype.slice.call(g.querySelectorAll(":scope > .row"));
      rows.sort(function (a, b) {
        if (mode === "default") return (+a.dataset.i) - (+b.dataset.i);
        var d = (+a.dataset.age) - (+b.dataset.age);
        return mode === "oldest" ? -d : d; // oldest: longest in state first
      });
      rows.forEach(function (r) { g.appendChild(r); });
    });
  }

  function applyAllSorts() {
    tagOrder();
    document.querySelectorAll("section .sort-btn").forEach(function (btn) {
      applySort(btn.closest("section"));
    });
  }

  var es = new EventSource("/events");
  es.onmessage = function (e) {
    var ev;
    try { ev = JSON.parse(e.data); } catch (_) { return; }

    if (ev.status === "running") {
      setBusy(true);
      var pct = ev.total > 0 ? Math.round((100 * ev.done) / ev.total) : 0;
      bar.style.width = pct + "%";
      var label = ev.phase || "working";
      if (ev.repo) label += " · " + ev.repo;
      if (ev.total > 0) label += " (" + ev.done + "/" + ev.total + ")";
      status.textContent = label;
      if (ev.tokens_in || ev.tokens_out) {
        tokens.textContent = "⛃ " + fmtTokens(ev.tokens_in) + " in / " + fmtTokens(ev.tokens_out) + " out";
      }
    } else if (ev.status === "done") {
      bar.style.width = "100%";
      status.textContent = "done";
      setBusy(false);
      reloadLanes();
      setTimeout(function () { bar.style.width = "0%"; status.textContent = ""; }, 1500);
    } else if (ev.status === "error") {
      status.textContent = "error: " + (ev.message || "unknown");
      setBusy(false);
    }
  };

  // Sort-cycle buttons. Delegated so they survive lanes fragment swaps.
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest(".sort-btn");
    if (!b) return;
    var section = b.closest("section");
    if (!section) return;
    var cur = sortModes[section.id] || "default";
    sortModes[section.id] = SORT_CYCLE[(SORT_CYCLE.indexOf(cur) + 1) % SORT_CYCLE.length];
    applySort(section);
  });

  // Copy buttons on draft-comment cards. Delegated on document so it keeps
  // working after the lanes fragment is swapped in.
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest(".copy-btn");
    if (!b) return;
    var src = document.getElementById(b.dataset.t);
    if (!src) return;
    var txt = src.textContent;
    function ok() {
      var prev = b.dataset.p || b.textContent;
      b.dataset.p = prev;
      b.textContent = "Copied ✓";
      b.classList.add("ok");
      setTimeout(function () { b.textContent = prev; b.classList.remove("ok"); }, 1200);
    }
    function fallback() {
      var ta = document.createElement("textarea");
      ta.value = txt;
      ta.style.position = "fixed"; ta.style.top = "0"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.focus(); ta.select();
      try { document.execCommand("copy"); ok(); } catch (_) { b.textContent = "Copy failed"; }
      document.body.removeChild(ta);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(txt).then(ok).catch(fallback);
    } else {
      fallback();
    }
  });

  refresh.addEventListener("click", function () {
    status.textContent = "starting…"; bar.style.width = "0%"; tokens.textContent = "";
    fetch("/refresh", { method: "POST" });
  });
  dispatch.addEventListener("click", function () {
    status.textContent = "starting…"; bar.style.width = "0%"; tokens.textContent = "";
    fetch("/dispatch", { method: "POST" });
  });
  cancel.addEventListener("click", function () {
    status.textContent = "cancelling…";
    fetch("/cancel", { method: "POST" });
  });

  applyAllSorts();
})();

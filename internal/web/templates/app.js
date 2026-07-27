(function () {
  function fmtTokens(n) {
    n = n || 0;
    if (n >= 1000) return Math.round(n / 1000) + "k";
    return "" + n;
  }

  var refresh = document.getElementById("refresh");
  var triage = document.getElementById("triage");
  var dispatch = document.getElementById("dispatch");
  var cancel = document.getElementById("cancel");
  var bar = document.getElementById("bar");
  var status = document.getElementById("status");
  var tokens = document.getElementById("tokens");
  var lanes = document.getElementById("lanes");

  // Jobs run independently — each button is disabled only while its own job is
  // in flight, so refreshing triage doesn't block a lanes refresh or dispatch.
  var running = {};

  function applyBusy() {
    refresh.disabled = !!running.refresh;
    triage.disabled = !!running.triage;
    dispatch.disabled = !!running.dispatch;
    cancel.hidden = !running.dispatch;
  }

  function anyRunning() {
    return running.refresh || running.triage || running.dispatch;
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
      running[ev.job] = true;
      applyBusy();
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
      delete running[ev.job];
      applyBusy();
      reloadLanes();
      if (!anyRunning()) {
        bar.style.width = "100%";
        status.textContent = "done";
        setTimeout(function () { bar.style.width = "0%"; status.textContent = ""; }, 1500);
      }
    } else if (ev.status === "error") {
      delete running[ev.job];
      applyBusy();
      status.textContent = "error: " + (ev.message || "unknown");
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

  // Push selected draft comments to GitHub as a pending (unsubmitted) review.
  // Delegated so it survives lanes fragment swaps.
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest(".push-btn");
    if (!b) return;
    var bar = b.closest(".push-bar");
    var box = b.closest(".cmts");
    if (!bar || !box) return;
    var out = bar.querySelector(".push-status");
    var sel = Array.prototype.slice
      .call(box.querySelectorAll(".cmt-sel:checked"))
      .map(function (c) { return +c.dataset.i; });
    if (!sel.length) {
      out.textContent = "select at least one comment";
      out.className = "push-status err";
      return;
    }
    b.disabled = true;
    out.textContent = "pushing…";
    out.className = "push-status";
    fetch("/review", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        repo: bar.dataset.repo,
        number: +bar.dataset.number,
        sha: bar.dataset.sha,
        comments: sel,
      }),
    })
      .then(function (r) {
        return r.json().catch(function () { return { ok: false, error: "HTTP " + r.status }; });
      })
      .then(function (res) {
        b.disabled = false;
        if (res.ok) {
          out.className = "push-status ok";
          out.textContent = "";
          var a = document.createElement("a");
          a.href = res.url;
          a.target = "_blank";
          a.rel = "noopener";
          a.textContent = "pending review created — submit on GitHub ↗";
          out.appendChild(a);
        } else {
          out.className = "push-status err";
          out.textContent = "error: " + (res.error || "unknown");
        }
      })
      .catch(function (err) {
        b.disabled = false;
        out.className = "push-status err";
        out.textContent = "error: " + err;
      });
  });

  // Keep the per-workup "all" checkbox and its comment checkboxes in sync.
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (!t.classList) return;
    var box = t.closest && t.closest(".cmts");
    if (!box) return;
    if (t.classList.contains("cmt-all")) {
      box.querySelectorAll(".cmt-sel").forEach(function (c) { c.checked = t.checked; });
    } else if (t.classList.contains("cmt-sel")) {
      var all = box.querySelector(".cmt-all");
      if (all) {
        var total = box.querySelectorAll(".cmt-sel").length;
        var on = box.querySelectorAll(".cmt-sel:checked").length;
        all.checked = total === on;
      }
    }
  });

  refresh.addEventListener("click", function () {
    status.textContent = "starting…"; bar.style.width = "0%"; tokens.textContent = "";
    fetch("/refresh", { method: "POST" });
  });
  triage.addEventListener("click", function () {
    status.textContent = "starting…"; bar.style.width = "0%"; tokens.textContent = "";
    fetch("/triage", { method: "POST" });
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

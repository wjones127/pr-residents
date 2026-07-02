(function () {
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
      .then(function (html) { lanes.innerHTML = html; });
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
        tokens.textContent = "⛃ " + ev.tokens_in + " in / " + ev.tokens_out + " out";
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
})();

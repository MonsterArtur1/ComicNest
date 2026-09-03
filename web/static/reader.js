// ComicNest page reader: one page at a time, zoom, keyboard/touch paging,
// progress reported to the server (same record OPDS readers use).
(function () {
    "use strict";

    const root = document.getElementById("reader");
    const issueID = root.dataset.issue;
    const total = parseInt(root.dataset.total, 10);
    const nextIssue = root.dataset.next !== "0" ? root.dataset.next : null;

    const stage = document.getElementById("stage");
    const img = document.getElementById("page");
    const slider = document.getElementById("slider");
    const pageNum = document.getElementById("page-num");
    const zoomLabel = document.getElementById("zoom-label");
    const loading = document.getElementById("loading");

    // --- zoom -----------------------------------------------------------
    // mode: "fit-h" (whole page visible), "fit-w" (page width = viewport,
    // scroll vertically) or "custom" (scale in percent, scroll both ways).
    const ZOOM_KEY = "comicnest.reader.zoom";
    let zoom = { mode: "fit-h", scale: 100 };
    try {
        const saved = JSON.parse(localStorage.getItem(ZOOM_KEY));
        if (saved && saved.mode) zoom = saved;
    } catch (e) { /* ignore */ }

    function applyZoom() {
        root.classList.remove("zoom-fit-h", "zoom-fit-w", "zoom-custom");
        root.classList.add("zoom-" + zoom.mode);
        if (zoom.mode === "custom") {
            img.style.width = (img.naturalWidth * zoom.scale / 100) + "px";
            zoomLabel.textContent = zoom.scale + "%";
        } else {
            img.style.width = "";
            zoomLabel.textContent = zoom.mode === "fit-h" ? "auto" : "szer.";
        }
        document.querySelectorAll("[data-zoom]").forEach(function (b) {
            b.classList.toggle("active", b.dataset.zoom === zoom.mode);
        });
        localStorage.setItem(ZOOM_KEY, JSON.stringify(zoom));
    }

    function setZoom(action) {
        if (action === "fit-h" || action === "fit-w") {
            zoom.mode = action;
        } else {
            // Entering custom mode starts from the size the page has now.
            if (zoom.mode !== "custom") {
                zoom.scale = Math.round(img.clientWidth / img.naturalWidth * 100) || 100;
                zoom.mode = "custom";
            }
            zoom.scale = action === "in"
                ? Math.min(400, zoom.scale + 20)
                : Math.max(20, zoom.scale - 20);
        }
        applyZoom();
    }

    // --- paging ---------------------------------------------------------
    let current = Math.min(Math.max(parseInt(root.dataset.start, 10) || 1, 1), total); // 1-based
    const cache = {};

    function pageURL(n) {
        return "/issues/" + issueID + "/pages/" + (n - 1) + "?track=0";
    }

    function preload(n) {
        if (n < 1 || n > total || cache[n]) return;
        const i = new Image();
        i.src = pageURL(n);
        cache[n] = i;
    }

    let progressTimer = null;
    function reportProgress(immediate) {
        clearTimeout(progressTimer);
        const send = function () {
            const body = new URLSearchParams({ page: String(current) });
            if (immediate && navigator.sendBeacon) {
                navigator.sendBeacon("/issues/" + issueID + "/progress", body);
            } else {
                fetch("/issues/" + issueID + "/progress", { method: "POST", body: body, keepalive: true })
                    .catch(function () { /* offline: progress is best effort */ });
            }
        };
        if (immediate) send(); else progressTimer = setTimeout(send, 400);
    }

    function show(n) {
        n = Math.min(Math.max(n, 1), total);
        current = n;
        loading.hidden = false;
        img.src = pageURL(n);
        pageNum.textContent = n;
        slider.value = n;
        document.title = "str. " + n + "/" + total + " — " + root.querySelector(".reader-title").textContent;
        stage.scrollTop = 0;
        stage.scrollLeft = 0;
        preload(n + 1);
        preload(n - 1);
        reportProgress(false);
    }

    img.addEventListener("load", function () {
        loading.hidden = true;
        applyZoom();
    });
    img.addEventListener("error", function () {
        loading.hidden = false;
        loading.textContent = "Nie udało się wczytać strony " + current;
    });

    // Past the last page nothing happens — the reader leaves when they want
    // (Esc / back arrow / "next issue" on the bottom bar).
    function next() {
        if (current < total) show(current + 1);
    }
    function prev() {
        if (current > 1) show(current - 1);
    }

    // --- chrome (bars) --------------------------------------------------
    let hideTimer = null;
    function showBars() {
        root.classList.remove("bars-hidden");
        clearTimeout(hideTimer);
        hideTimer = setTimeout(function () { root.classList.add("bars-hidden"); }, 2500);
    }
    function toggleBars() {
        if (root.classList.contains("bars-hidden")) showBars();
        else { clearTimeout(hideTimer); root.classList.add("bars-hidden"); }
    }

    // --- events ---------------------------------------------------------
    root.addEventListener("click", function (e) {
        const el = e.target.closest("[data-nav], [data-zoom]");
        if (!el) return;
        if (el.dataset.zoom) { setZoom(el.dataset.zoom); showBars(); return; }
        switch (el.dataset.nav) {
            case "next": next(); break;
            case "prev": prev(); break;
            case "menu": toggleBars(); break;
        }
    });

    slider.addEventListener("input", function () { show(parseInt(slider.value, 10)); showBars(); });

    document.getElementById("fullscreen").addEventListener("click", function () {
        if (document.fullscreenElement) document.exitFullscreen();
        else root.requestFullscreen && root.requestFullscreen();
    });

    document.addEventListener("keydown", function (e) {
        if (e.target.tagName === "INPUT" && e.target !== slider) return;
        switch (e.key) {
            case "ArrowRight": case "PageDown": case " ": case "d": e.preventDefault(); next(); break;
            case "ArrowLeft": case "PageUp": case "a": e.preventDefault(); prev(); break;
            case "Home": show(1); break;
            case "End": show(total); break;
            case "+": case "=": setZoom("in"); break;
            case "-": case "_": setZoom("out"); break;
            case "0": setZoom("fit-h"); break;
            case "w": setZoom("fit-w"); break;
            case "f": document.getElementById("fullscreen").click(); break;
            case "Escape":
                if (document.fullscreenElement) break; // browser handles it
                reportProgress(true);
                window.location.href = root.dataset.back;
                break;
            default: return;
        }
        showBars();
    });

    // Wheel: with the whole page visible it turns pages; otherwise it scrolls.
    stage.addEventListener("wheel", function (e) {
        if (e.ctrlKey) { e.preventDefault(); setZoom(e.deltaY < 0 ? "in" : "out"); return; }
        const scrollable = stage.scrollHeight > stage.clientHeight + 2;
        if (!scrollable) {
            e.preventDefault();
            if (e.deltaY > 0) next(); else if (e.deltaY < 0) prev();
        }
    }, { passive: false });

    let touchX = null, touchY = null;
    stage.addEventListener("touchstart", function (e) {
        touchX = e.changedTouches[0].clientX; touchY = e.changedTouches[0].clientY;
    }, { passive: true });
    stage.addEventListener("touchend", function (e) {
        if (touchX === null) return;
        const dx = e.changedTouches[0].clientX - touchX;
        const dy = e.changedTouches[0].clientY - touchY;
        touchX = touchY = null;
        if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy) * 1.5) {
            if (dx < 0) next(); else prev();
        }
    }, { passive: true });

    document.addEventListener("mousemove", showBars);
    window.addEventListener("pagehide", function () { reportProgress(true); });
    document.addEventListener("visibilitychange", function () {
        if (document.visibilityState === "hidden") reportProgress(true);
    });

    // --- go ---------------------------------------------------------------
    applyZoom();
    show(current);
    showBars();
    if (nextIssue) {
        // Warm the next issue's first page so "Następny zeszyt" opens instantly.
        const warm = new Image();
        warm.src = "/issues/" + nextIssue + "/pages/0?track=0";
    }
})();

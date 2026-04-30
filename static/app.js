const runBtn = document.getElementById("runBtn");
const promptEl = document.getElementById("prompt");
const ratioEl = document.getElementById("ratio");
const msgEl = document.getElementById("msg");
const progressBarEl = document.getElementById("progressBar");
const progressTextEl = document.getElementById("progressText");
const stateBadgeEl = document.getElementById("stateBadge");
const previewWrapEl = document.getElementById("previewWrap");

let timer = null;

function setState(state) {
  stateBadgeEl.className = `badge ${state}`;
  stateBadgeEl.textContent = state;
}

function setProgress(value) {
  const safe = Math.max(0, Math.min(100, Number(value) || 0));
  progressBarEl.style.width = `${safe}%`;
  progressTextEl.textContent = `progress: ${safe.toFixed(1)}%`;
}

function setMessage(text, isError = false) {
  msgEl.textContent = text || "";
  msgEl.style.color = isError ? "#ffb4bc" : "#a8bfd8";
}

function renderImage(url) {
  previewWrapEl.innerHTML = "";
  const img = document.createElement("img");
  img.src = `${url}?t=${Date.now()}`;
  img.alt = "Generated image";
  previewWrapEl.appendChild(img);
}

async function pollTask(taskId) {
  const res = await fetch(`/api/v1/generate/${taskId}`);
  if (!res.ok) {
    throw new Error(`poll failed: ${res.status}`);
  }
  const data = await res.json();
  setState(data.status || "unknown");
  setProgress(data.progress || 0);

  if (data.status === "succeeded" && data.image_url) {
    clearInterval(timer);
    timer = null;
    renderImage(data.image_url);
    setMessage("Done. Image rendered successfully.");
  }

  if (data.status === "failed") {
    clearInterval(timer);
    timer = null;
    setMessage(data.error || "Generation failed", true);
  }
}

runBtn.addEventListener("click", async () => {
  if (timer) {
    clearInterval(timer);
    timer = null;
  }

  const prompt = promptEl.value.trim();
  const aspect_ratio = ratioEl.value;

  if (!prompt) {
    setMessage("Please provide a prompt first.", true);
    return;
  }

  setState("queued");
  setProgress(0);
  setMessage("Submitting request...");

  try {
    const res = await fetch("/api/v1/generate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt, aspect_ratio })
    });

    const data = await res.json();
    if (!res.ok) {
      throw new Error(data.detail || `submit failed (${res.status})`);
    }

    const taskId = data.task_id;
    setMessage(`Task accepted: ${taskId.slice(0, 8)}...`);
    timer = setInterval(() => {
      pollTask(taskId).catch((err) => {
        clearInterval(timer);
        timer = null;
        setMessage(err.message, true);
      });
    }, 2200);
    await pollTask(taskId);
  } catch (err) {
    setState("failed");
    setMessage(err.message || "Request failed", true);
  }
});

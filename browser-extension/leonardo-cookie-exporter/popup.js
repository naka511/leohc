const LEONARDO_HOSTS = ["app.leonardo.ai", "api.leonardo.ai", "leonardo.ai"];

const IMPORTANT_COOKIE_ORDER = [
  "__Secure-better-auth.session_token",
  "__Secure-better-auth.session_data.0",
  "__Secure-better-auth.session_data.1",
  "__Secure-better-auth.session_data.2",
  "__Secure-better-auth.session_data.3",
  "CF_Access_Token",
];

const exportJsonBtn = document.getElementById("exportJsonBtn");
const copyCookieBtn = document.getElementById("copyCookieBtn");
const includeAllCookies = document.getElementById("includeAllCookies");
const prettyJson = document.getElementById("prettyJson");
const statusEl = document.getElementById("status");

function setStatus(message, type = "") {
  statusEl.textContent = message;
  statusEl.className = `status${type ? ` ${type}` : ""}`;
}

function uniqueCookies(cookies) {
  const map = new Map();
  for (const cookie of cookies) {
    const key = [cookie.domain, cookie.path, cookie.name].join("|");
    if (!map.has(key)) {
      map.set(key, cookie);
    }
  }
  return Array.from(map.values());
}

async function getAllLeonardoCookies() {
  const groups = await Promise.all(
    LEONARDO_HOSTS.map((domain) => chrome.cookies.getAll({ domain }))
  );
  return uniqueCookies(groups.flat());
}

function sortCookies(cookies, keepAll) {
  const importantMatcher = keepAll
    ? IMPORTANT_COOKIE_ORDER
    : IMPORTANT_COOKIE_ORDER;

  const importantRank = (name) => {
    const directIndex = importantMatcher.indexOf(name);
    if (directIndex >= 0) return directIndex;
    if (name.startsWith("__Secure-better-auth.session_data.")) {
      return 100 + Number(name.split(".").pop() || 0);
    }
    return 1000;
  };

  return [...cookies].sort((a, b) => {
    const rankDiff = importantRank(a.name) - importantRank(b.name);
    if (rankDiff !== 0) return rankDiff;
    if (a.name !== b.name) return a.name.localeCompare(b.name);
    if (a.domain !== b.domain) return a.domain.localeCompare(b.domain);
    return a.path.localeCompare(b.path);
  });
}

function filterCookies(cookies, keepAll) {
  if (keepAll) return cookies;
  return cookies.filter((cookie) => {
    return (
      cookie.name === "__Secure-better-auth.session_token" ||
      cookie.name === "CF_Access_Token" ||
      cookie.name.startsWith("__Secure-better-auth.session_data.")
    );
  });
}

function toCookieString(cookies) {
  return cookies
    .map((cookie) => `${cookie.name}=${cookie.value}`)
    .join("; ");
}

function buildExportPayload(cookieString) {
  return {
    cookie: cookieString,
  };
}

async function collectCookieString() {
  const allCookies = await getAllLeonardoCookies();
  const filtered = filterCookies(allCookies, includeAllCookies.checked);
  const sorted = sortCookies(filtered, includeAllCookies.checked);
  const cookieString = toCookieString(sorted);

  if (!cookieString) {
    throw new Error("未读取到 Leonardo Cookie。请先在浏览器中登录 app.leonardo.ai。");
  }

  return {
    cookieString,
    cookieCount: sorted.length,
  };
}

async function exportJson() {
  exportJsonBtn.disabled = true;
  copyCookieBtn.disabled = true;
  setStatus("正在读取 Leonardo Cookie...");

  try {
    const { cookieString, cookieCount } = await collectCookieString();
    const payload = buildExportPayload(cookieString);
    const json = JSON.stringify(payload, null, prettyJson.checked ? 2 : 0);
    const blob = new Blob([json], { type: "application/json" });
    const url = URL.createObjectURL(blob);

    const filename = `leonardo-cookie-${new Date()
      .toISOString()
      .replace(/[:.]/g, "-")}.json`;

    await chrome.downloads.download({
      url,
      filename,
      saveAs: true,
    });

    setStatus(`导出成功，共写入 ${cookieCount} 个 Cookie。`, "success");
    setTimeout(() => URL.revokeObjectURL(url), 5000);
  } catch (error) {
    setStatus(error.message || "导出失败。", "error");
  } finally {
    exportJsonBtn.disabled = false;
    copyCookieBtn.disabled = false;
  }
}

async function copyCookie() {
  exportJsonBtn.disabled = true;
  copyCookieBtn.disabled = true;
  setStatus("正在读取 Leonardo Cookie...");

  try {
    const { cookieString, cookieCount } = await collectCookieString();
    await navigator.clipboard.writeText(cookieString);
    setStatus(`已复制 Cookie 字符串，共 ${cookieCount} 个 Cookie。`, "success");
  } catch (error) {
    setStatus(error.message || "复制失败。", "error");
  } finally {
    exportJsonBtn.disabled = false;
    copyCookieBtn.disabled = false;
  }
}

exportJsonBtn.addEventListener("click", exportJson);
copyCookieBtn.addEventListener("click", copyCookie);

// Turns the engine's English log lines into short Russian sentences.
import type { LogEntry } from "@/types"

export type EventKind = "ok" | "warn" | "down" | "cert" | "info"

interface Rule {
  re: RegExp
  kind: EventKind
  text: (m: RegExpMatchArray) => string
  /** Worth showing among the dashboard's events. */
  notable?: boolean
}

const rules: Rule[] = [
  { re: /^\[BACKENDS\] .+?: active backends updated to: \[(.*)\]$/, kind: "info", text: (m) => `Трафик идёт на: ${m[1].replace(/\(/g, " (")}` },
  { re: /^\[FAILOVER\] .+?: primary backends recovered$/, kind: "ok", notable: true, text: () => "Основные узлы вернулись, резерв снова ждёт" },
  { re: /^\[FAILOVER\] .+?: all primary backends DOWN, switching to backup nodes$/, kind: "warn", notable: true, text: () => "Основных узлов не осталось — трафик ушёл на резерв" },
  { re: /^\[CRITICAL\] .+?: ALL backends \(primary \+ backup\) are DOWN$/, kind: "down", notable: true, text: () => "Недоступны все узлы, включая резерв" },
  { re: /^HealthCheck FAILED for (.+?) \((.+?)\) in LB .+$/, kind: "down", notable: true, text: (m) => `Узел ${m[1]} (${m[2]}) не прошёл проверку и выведен из работы` },
  { re: /^HealthCheck OK for (.+?) \((.+?)\) in LB .+$/, kind: "ok", notable: true, text: (m) => `Узел ${m[1]} (${m[2]}) снова здоров` },
  { re: /^ACME certificate successfully issued for domains: \[(.*)\]$/, kind: "cert", notable: true, text: (m) => `Let's Encrypt выдал сертификат: ${m[1]}` },
  { re: /^Failed to manage ACME domains for .+?: (.+)$/, kind: "down", notable: true, text: (m) => `Не удалось получить сертификат: ${m[1]}` },
  { re: /^Failed to start LB (.+?): (.+)$/, kind: "down", notable: true, text: (m) => `«${m[1]}» не запустился: ${m[2]}` },
  { re: /^Failed to connect to backend (.+?): (.+)$/, kind: "warn", text: (m) => `Не удалось подключиться к узлу ${m[1]}` },
  { re: /^Proxy error to (.+?): (.+)$/, kind: "warn", text: (m) => `Узел не ответил на запрос ${m[1]}` },
  { re: /^Redirecting HTTP on (.+) to HTTPS$/, kind: "info", text: (m) => `Переадресация HTTP с ${m[1]} на HTTPS` },
  { re: /^HTTP\/3 \(QUIC\) LoadBalancer .+ listening on (.+)$/, kind: "info", text: (m) => `HTTP/3 принимает на ${m[1]}` },
  { re: /^DNS LoadBalancer .+ listening on (.+?) \(UDP \+ TCP\)$/, kind: "info", text: (m) => `Принимает DNS на ${m[1]} по UDP и TCP` },
  { re: /^(?:HTTPS|HTTP|TCP|UDP) LoadBalancer .+ listening on (.+)$/, kind: "info", text: (m) => `Принимает соединения на ${m[1]}` },
  { re: /^Running .+ in TLS Passthrough \(TCP\) mode$/, kind: "info", text: () => "HTTPS без расшифровки — TLS идёт до узлов как есть" },
  { re: /no certificate configured, using a self-signed one$/, kind: "warn", text: () => "Сертификат не задан — используется самоподписанный" },
  { re: /^API Server listening on (.+)$/, kind: "info", text: (m) => `API слушает ${m[1]}` },
  { re: /^Starting Engine/, kind: "info", text: () => "Балансировщик запускается" },
  {
    re: /^.+?: (\d+) new UDP sessions refused in the last minute: the limit of (\d+) sessions is reached$/,
    kind: "warn",
    notable: true,
    text: (m) => `Достигнут предел UDP-сессий (${m[2]}): за минуту отброшено ${m[1]} новых клиентов. Для DNS включите режим «DNS-сервер»`,
  },
  {
    re: /^.+?: (\d+) connections refused by the access rules in the last .+?; most from: (.+?)(?:; domains: (.+))?$/,
    kind: "warn",
    notable: true,
    text: (m) =>
      `Фильтр доступа отсёк ${m[1]} подключений за минуту. Чаще всего: ${m[2].replace("deny list", "список запрещённых").replace("unknown country", "страна не определена")}` +
      (m[3] ? `. Спрашивали: ${m[3]}` : ""),
  },
  { re: /^.+?: the country database is not loaded, so the country filter is letting everyone in/, kind: "warn", notable: true, text: () => "База стран не загружена — фильтр по странам пока пропускает всех. Загрузите её в Настройках" },
  { re: /^Geo database: (.+?) updated \((\d+) IPv4 and (\d+) IPv6 ranges\)$/, kind: "info", text: (m) => `База стран: данные ${m[1]} обновлены` },
  { re: /^Geo database: (.+?) was not updated: (.+)$/, kind: "warn", notable: true, text: (m) => `База стран: ${m[1]} не обновлена — ${m[2]}` },
  { re: /^Geo database update failed, next try in an hour: (.+)$/, kind: "warn", text: () => "База стран не обновилась, повторю через час" },
  { re: /^(\d+) failed connections to backend (.+?) since the last message; latest: (.+)$/, kind: "warn", text: (m) => `${m[1]} неудачных подключений к узлу ${m[2]}` },
  { re: /^Backend (.+?) accepts connections again$/, kind: "ok", text: (m) => `Узел ${m[1]} снова принимает подключения` },
  { re: /^.+?: (\d+) failed TLS handshakes in the last .+?; most common: (.+)$/, kind: "warn", text: (m) => `${m[1]} неудачных TLS-рукопожатий за минуту. Чаще всего: ${m[2]}` },
  { re: /^Telegram alert (?:failed|rejected)/, kind: "warn", notable: true, text: () => "Не удалось отправить уведомление в Telegram" },
]

export interface HumanEvent {
  text: string
  kind: EventKind
  notable: boolean
}

export function humanize(entry: LogEntry): HumanEvent {
  for (const r of rules) {
    const m = entry.message.match(r.re)
    if (m) return { text: r.text(m), kind: r.kind, notable: !!r.notable }
  }
  const kind: EventKind = entry.level === "ERROR" ? "down" : entry.level === "WARN" ? "warn" : "info"
  return { text: entry.message, kind, notable: entry.level !== "INFO" }
}

import { useEffect, useState, useRef } from "react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"

const API_BASE = () => `http://${window.location.hostname}:8080`

interface LogEntry {
  level: string
  message: string
  timestamp: string
  lb_name?: string
}

export default function Logs() {
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [filter, setFilter] = useState("ALL")
  const logsEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const fetchLogs = async () => {
      try {
        const res = await fetch(`${API_BASE()}/api/v1/logs`, {
          headers: {
            "Authorization": `Bearer ${localStorage.getItem("token")}`
          }
        })
        if (res.ok) {
          const data = await res.json()
          setLogs(data || [])
        }
      } catch (err) {
        console.error("Failed to fetch logs", err)
      }
    }

    fetchLogs()
    const interval = setInterval(fetchLogs, 2000)
    return () => clearInterval(interval)
  }, [])

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [logs])

  const filteredLogs = logs.filter(log => filter === "ALL" || log.level === filter)

  const getLogColor = (level: string) => {
    switch (level.toUpperCase()) {
      case "INFO": return "text-blue-400"
      case "WARN": return "text-yellow-400"
      case "ERROR": return "text-red-400"
      default: return "text-gray-300"
    }
  }

  return (
    <div className="grid gap-4 md:gap-8">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold tracking-tight">System Logs</h2>
        <div className="flex space-x-2">
          {["ALL", "INFO", "WARN", "ERROR"].map(f => (
            <Badge 
              key={f}
              variant={filter === f ? "default" : "outline"}
              className="cursor-pointer"
              onClick={() => setFilter(f)}
            >
              {f}
            </Badge>
          ))}
        </div>
      </div>

      <Card className="bg-zinc-950 border-zinc-800 text-zinc-50 font-mono text-sm shadow-xl">
        <CardHeader className="border-b border-zinc-800 py-3">
          <CardTitle className="text-xs text-zinc-400 flex items-center gap-2">
            <div className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
            Live Logs
          </CardTitle>
        </CardHeader>
        <CardContent className="p-4 h-[600px] overflow-y-auto">
          {filteredLogs.length === 0 ? (
            <div className="text-zinc-500 italic">No logs found...</div>
          ) : (
            <div className="space-y-1">
              {filteredLogs.map((log, i) => (
                <div key={i} className="flex gap-4 hover:bg-zinc-900 px-2 py-1 rounded">
                  <span className="text-zinc-500 whitespace-nowrap">
                    {new Date(log.timestamp).toISOString().replace('T', ' ').substring(0, 19)}
                  </span>
                  <span className={`w-12 font-bold ${getLogColor(log.level)}`}>
                    {log.level.padEnd(5)}
                  </span>
                  {log.lb_name && (
                    <span className="text-blue-300 font-bold">[{log.lb_name}]</span>
                  )}
                  <span className="text-zinc-300 break-all">{log.message}</span>
                </div>
              ))}
              <div ref={logsEndRef} />
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

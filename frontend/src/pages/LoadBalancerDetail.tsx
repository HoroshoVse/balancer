import { useEffect, useState } from "react"
import { useParams, useNavigate } from "react-router-dom"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from "recharts"

const API_BASE = () => `http://${window.location.hostname}:8080`

export default function LoadBalancerDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const [lb, setLb] = useState<any>(null)
  const [history, setHistory] = useState<any[]>([])
  const [logs, setLogs] = useState<any[]>([])
  useEffect(() => {
    const fetchLb = async () => {
      try {
        const res = await fetch(`${API_BASE()}/api/v1/load-balancers`, {
          headers: { "Authorization": `Bearer ${localStorage.getItem("token")}` }
        })
        if (res.ok) {
          const data = await res.json()
          const found = data.find((l: any) => l.id === Number(id))
          if (found) setLb(found)
        }
      } catch (err) {
        console.error("Failed to fetch lb", err)
      }
    }
    fetchLb()
    const interval = setInterval(fetchLb, 5000)
    return () => clearInterval(interval)
  }, [id])

  useEffect(() => {
    if (!lb?.name) return
    const fetchLogs = async () => {
      try {
        const res = await fetch(`${API_BASE()}/api/v1/logs?lb_name=${lb.name}`, {
          headers: { "Authorization": `Bearer ${localStorage.getItem("token")}` }
        })
        if (res.ok) {
          const data = await res.json()
          setLogs(data)
        }
      } catch (err) {
        console.error("Failed to fetch logs", err)
      }
    }
    fetchLogs()
    const interval = setInterval(fetchLogs, 5000)
    return () => clearInterval(interval)
  }, [lb?.name])

  useEffect(() => {
    const fetchHistory = async () => {
      try {
        const res = await fetch(`${API_BASE()}/api/v1/load-balancers/history?id=${id}`, {
          headers: { "Authorization": `Bearer ${localStorage.getItem("token")}` }
        })
        if (res.ok) {
          const data = await res.json()
          setHistory(data)
        }
      } catch (err) {
        console.error("Failed to fetch history", err)
      }
    }
    fetchHistory()
    const interval = setInterval(fetchHistory, 5000)
    return () => clearInterval(interval)
  }, [id])

  if (!lb) return <div className="p-8 text-center text-muted-foreground">Loading...</div>

  const latestHistory = history.length > 0 ? history[history.length - 1] : null;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <Button variant="outline" size="sm" onClick={() => navigate("/lbs")}>← Back</Button>
          <h2 className="text-3xl font-bold tracking-tight">{lb.name}</h2>
          <Badge variant={lb.status === 'UP' ? 'default' : 'secondary'}>{lb.protocol?.toUpperCase()}</Badge>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Requests per Second</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{Math.round(latestHistory?.rps || 0)}</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Avg Latency</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{Math.round(latestHistory?.avg_latency_ms || 0)} ms</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Error Rate</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-red-500">{latestHistory?.error_rate?.toFixed(2) || 0}%</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Backend Nodes</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{lb.backend_group?.backends?.length || 0}</div>
          </CardContent>
        </Card>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Requests per Second</CardTitle>
          </CardHeader>
          <CardContent className="h-[300px]">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={history}>
                <CartesianGrid strokeDasharray="3 3" />
                <XAxis dataKey="timestamp" tickFormatter={(tick) => { const d = new Date(Number(tick)); return isNaN(d.getTime()) ? "" : d.toLocaleTimeString(); }} />
                <YAxis />
                <Tooltip 
                  labelFormatter={(label: any) => { const d = new Date(Number(label)); return isNaN(d.getTime()) ? "" : d.toLocaleTimeString(); }} 
                  formatter={(value: any, name: any) => [Math.round(Number(value) || 0), name]} 
                />
                <Line type="monotone" dataKey="rps" stroke="#8884d8" strokeWidth={2} dot={false} />
              </LineChart>
            </ResponsiveContainer>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Latency (ms)</CardTitle>
          </CardHeader>
          <CardContent className="h-[300px]">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={history}>
                <CartesianGrid strokeDasharray="3 3" />
                <XAxis dataKey="timestamp" tickFormatter={(tick) => { const d = new Date(Number(tick)); return isNaN(d.getTime()) ? "" : d.toLocaleTimeString(); }} />
                <YAxis />
                <Tooltip 
                  labelFormatter={(label: any) => { const d = new Date(Number(label)); return isNaN(d.getTime()) ? "" : d.toLocaleTimeString(); }} 
                  formatter={(value: any, name: any) => [Math.round(Number(value) || 0), name]} 
                />
                <Line type="monotone" dataKey="avg_latency_ms" stroke="#82ca9d" strokeWidth={2} dot={false} />
              </LineChart>
            </ResponsiveContainer>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Backend Nodes Health</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-4">
            {lb.backend_group?.backends?.map((b: any, i: number) => (
              <div key={b.id || i} className="flex items-center justify-between p-4 border rounded-lg">
                <div className="flex items-center gap-4">
                  <div className={`w-3 h-3 rounded-full ${b.status === 'UP' ? 'bg-green-500' : (b.status === 'DOWN' ? 'bg-red-500' : 'bg-gray-500')}`} />
                  <div>
                    <div className="font-semibold">{b.name || b.address}</div>
                    <div className="text-sm text-muted-foreground">{b.address}:{b.port}</div>
                  </div>
                </div>
                <div className="flex gap-4 items-center">
                  <div className="flex flex-col items-end gap-1">
                    <span className="text-sm font-medium">{Math.round(b.metrics?.rps || 0)} RPS</span>
                    <span className="text-sm text-muted-foreground">{Math.round(b.metrics?.avg_latency_ms || 0)} ms</span>
                  </div>
                  <div className="flex gap-2">
                    <Badge variant={b.enabled ? "outline" : "secondary"}>{b.enabled ? "ENABLED" : "DISABLED"}</Badge>
                    {b.backup && <Badge variant="secondary">BACKUP</Badge>}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Recent Logs</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="bg-black text-green-400 p-4 rounded-md font-mono text-sm h-64 overflow-y-auto">
            {logs.length === 0 ? (
              <div className="text-gray-500">No logs found...</div>
            ) : (
              logs.map((log: any, i: number) => (
                <div key={i} className="mb-1">
                  <span className="text-gray-500">[{new Date(log.timestamp).toLocaleString()}]</span>{" "}
                  <span className={log.level === 'ERROR' ? 'text-red-500' : log.level === 'WARN' ? 'text-yellow-500' : 'text-blue-400'}>
                    [{log.level}]
                  </span>{" "}
                  <span className="text-gray-300">[{log.lb_name}]</span>{" "}
                  {log.message}
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}

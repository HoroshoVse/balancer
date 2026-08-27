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

  if (!lb) return <div className="p-8 text-center text-muted-foreground">Loading...</div>

  // Dummy history for charts since we don't have historical data API yet
  const chartData = [
    { time: "10:00", rps: Math.max(0, (lb.metrics?.total_requests || 0) - 50), latency: Math.max(0, (lb.metrics?.average_latency_ms || 0) - 5) },
    { time: "10:01", rps: Math.max(0, (lb.metrics?.total_requests || 0) - 20), latency: Math.max(0, (lb.metrics?.average_latency_ms || 0) - 2) },
    { time: "10:02", rps: Math.max(0, (lb.metrics?.total_requests || 0) - 40), latency: Math.max(0, (lb.metrics?.average_latency_ms || 0) - 10) },
    { time: "10:03", rps: Math.max(0, (lb.metrics?.total_requests || 0) - 10), latency: Math.max(0, (lb.metrics?.average_latency_ms || 0) + 5) },
    { time: "10:04", rps: lb.metrics?.total_requests || 0, latency: lb.metrics?.average_latency_ms || 0 },
  ]

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
            <CardTitle className="text-sm font-medium">Total Requests (RPS)</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{lb.metrics?.total_requests || 0}</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Avg Latency</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{lb.metrics?.average_latency_ms || 0} ms</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Error Rate</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-red-500">{lb.metrics?.error_rate_percent?.toFixed(2) || 0}%</div>
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
              <LineChart data={chartData}>
                <CartesianGrid strokeDasharray="3 3" />
                <XAxis dataKey="time" />
                <YAxis />
                <Tooltip />
                <Line type="monotone" dataKey="rps" stroke="#8884d8" strokeWidth={2} />
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
              <LineChart data={chartData}>
                <CartesianGrid strokeDasharray="3 3" />
                <XAxis dataKey="time" />
                <YAxis />
                <Tooltip />
                <Line type="monotone" dataKey="latency" stroke="#82ca9d" strokeWidth={2} />
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
                <div className="flex gap-2">
                  <Badge variant={b.enabled ? "outline" : "secondary"}>{b.enabled ? "ENABLED" : "DISABLED"}</Badge>
                  {b.backup && <Badge variant="secondary">BACKUP</Badge>}
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}

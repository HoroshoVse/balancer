import { useState, useEffect } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { toast } from "sonner"


export function Settings() {
  const [token, setToken] = useState("")
  const [chatId, setChatId] = useState("")
  const [enabled, setEnabled] = useState(false)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const API_URL = `http://${window.location.hostname}:8080/api/v1/settings`
    fetch(API_URL, {
      headers: { "Authorization": `Bearer ${localStorage.getItem("token")}` }
    })
      .then(r => r.json())
      .then(data => {
        if (data) {
          setToken(data.telegram_bot_token || "")
          setChatId(data.telegram_chat_id || "")
          setEnabled(data.notifications_enabled || false)
        }
      })
      .finally(() => setLoading(false))
  }, [])

  const handleSave = async () => {
    try {
      const API_URL = `http://${window.location.hostname}:8080/api/v1/settings/update`
      const res = await fetch(API_URL, {
        method: "POST",
        headers: { 
          "Authorization": `Bearer ${localStorage.getItem("token")}`,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          telegram_bot_token: token,
          telegram_chat_id: chatId,
          notifications_enabled: enabled
        })
      })
      if (!res.ok) throw new Error("Failed to save settings")
      toast.success("Settings saved successfully")
    } catch (err) {
      toast.error("Error saving settings")
    }
  }

  if (loading) return <div>Loading...</div>

  return (
    <div className="flex flex-col gap-4">
      <h2 className="text-2xl font-bold tracking-tight">Settings</h2>
      
      <Card>
        <CardHeader>
          <CardTitle>Telegram Notifications</CardTitle>
          <CardDescription>
            Configure a Telegram bot to receive alerts when your backend servers go down or recover.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center gap-2">
            <input 
              type="checkbox" 
              id="enable-notifs"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
              className="h-4 w-4 rounded border-gray-300 text-primary focus:ring-primary"
            />
            <label htmlFor="enable-notifs" className="text-sm font-medium leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-70">
              Enable Telegram Alerts
            </label>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Bot Token</label>
            <Input 
              placeholder="1234567890:AAH_xyz..." 
              value={token} 
              onChange={e => setToken(e.target.value)} 
            />
            <p className="text-xs text-muted-foreground">Talk to @BotFather on Telegram to create a bot and get this token.</p>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Chat ID</label>
            <Input 
              placeholder="e.g. 12345678" 
              value={chatId} 
              onChange={e => setChatId(e.target.value)} 
            />
            <p className="text-xs text-muted-foreground">The ID of the chat or channel where alerts will be sent.</p>
          </div>

          <Button onClick={handleSave} className="mt-4">Save Settings</Button>
        </CardContent>
      </Card>
    </div>
  )
}

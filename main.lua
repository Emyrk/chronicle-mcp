local ui = require("chronicle.v1.ui")
local damage = require("chronicle.v1.events.damage")

local metric = ui.metric(UIParent, "damage-total", "Damage", 0, 16, -16)
local total = 0

damage.onEvent(function(event)
  total = total + event.amount
  metric.set(total)
end)

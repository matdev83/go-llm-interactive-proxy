import re

with open('internal/plugins/features/refworkspaceguard/config_test.go', 'r') as f:
    content = f.read()

content = content.replace("DirtyTree:   false, // explicit false in yaml override", "DirtyTree:   false")

with open('internal/plugins/features/refworkspaceguard/config_test.go', 'w') as f:
    f.write(content)

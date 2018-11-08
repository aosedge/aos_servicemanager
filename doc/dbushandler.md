# D-Bus handler

`dbushandler` implements D-Bus server which provides VIS client permissions. The permissions is provided with AOS service image during service install and stored in the SM database.

`dbushandler` supports following methods:
* `GetPermissions` - returns permissions by client token (service id). On this request `dbushandler` get permissions from the database and return to the D-Bus client (VIS).

D-Bus retrospect file implemented by `dbushandler`:

```xml
<!DOCTYPE node PUBLIC
    "-//freedesktop//DTD D-BUS Object Introspection 1.0//EN"
    "http://www.freedesktop.org/standards/dbus/1.0/introspect.dtd" >
<node xmlns:doc="http://www.freedesktop.org/dbus/1.0/doc.dtd">
  <interface name="com.epam.aos.vis">
    <method name="GetPermissions">
      <arg name="token" direction="in" type="s">
        <doc:doc><doc:summary>VIS client token (service id)</doc:summary></doc:doc>
      </arg>
      <arg name="permissions" direction="out" type="s">
        <doc:doc><doc:summary>VIS client permissions</doc:summary></doc:doc>
      </arg>
      <arg name="status" direction="out" type="s">
        <doc:doc><doc:summary>Status of getting VIS permissions: OK or error</doc:summary></doc:doc>
      </arg>
      <doc:doc>
        <doc:description>
          <doc:para>
            Returns VIS client permission
          </doc:para>
        </doc:description>
      </doc:doc>
    </method>
  </interface>
</node>
```
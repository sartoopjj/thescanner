# R8 is enabled in release builds (see app/build.gradle minifyEnabled=true).
# These rules tell it which symbols are reachable only via JNI / reflection
# so it doesn't strip them.

# gomobile-generated bindings — Kotlin calls into these, and the Go side
# calls back via reflective method lookup.
-keep class mobile.** { *; }
-keep class go.** { *; }

# WebView JS-interface callbacks. We don't currently add any with
# addJavascriptInterface, but if we ever do, the methods must survive
# minification or the JS bridge silently breaks.
-keepclassmembers class * {
    @android.webkit.JavascriptInterface <methods>;
}

# Activity / view classes the manifest names. Defensive — AGP normally
# adds these, but make it explicit so future ProGuard tweaks don't
# accidentally rename MainActivity and break the launcher intent.
-keep class com.thescanner.android.MainActivity { *; }

# Strip log / debug calls in release to shave a tiny bit off and stop
# leaking strings into the binary that reverse-engineers can grep.
-assumenosideeffects class android.util.Log {
    public static int v(...);
    public static int d(...);
    public static int i(...);
}
